package chat

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/errorx"
)

var (
	// ErrNoWorker 表示没有调用 Serve 的实例接收请求，例如 worker 都没有运行或正在停止（503）。
	ErrNoWorker = errorx.Define(errorx.Unavailable, "ai.no_worker")
	// ErrRunOptions 表示使用 RunOnWorkers 时传入了运行选项：agent.RunOption 不能跨实例传递。
	ErrRunOptions = errors.New("chat: run options cannot be sent to workers; set them on the agent")
)

// dispatchTimeout 是等待 worker 处理请求的时限。Approve 在 worker 上执行工具，可能需要一段时间。
var dispatchTimeout = 2 * time.Minute

// beforeDone 供测试在 worker 回复 done 之前插入等待，例如等运行结束。
var beforeDone func(*Run)

// RunOnWorkers 让本实例不执行运行：Start、Notify、Approve、Reject，以及基于它们的 Submit 和 Send，
// 把请求转交给调用了 Runs.Serve 的实例（通常是 worker 进程）。返回的运行是 worker 上那次运行的镜像，
// 事件、插话、停止的用法与本地的运行相同。需要 Cluster，否则 NewRuns panic。
//
// 请求经由广播转交、不排队：没有实例在 Serve 时返回 ErrNoWorker（503），由客户端稍后重试。
// worker 停止时等待进行中的运行结束（见 Shutdown），崩溃时进行中的运行随之结束，不会在其他实例上恢复，
// 这与在 HTTP 进程中运行时相同。
//
// 运行选项不能跨实例传递，传入时返回 ErrRunOptions；需要的话在 worker 创建的 agent 上设置。
func RunOnWorkers() Option { return func(rs *Runs) { rs.workers = true } }

const (
	opStart   = "start"
	opNotify  = "notify"
	opApprove = "approve"
	opReject  = "reject"
)

// dispatchReq 是转交给 worker 的请求。
type dispatchReq struct {
	ID      string         `json:"id"`
	Op      string         `json:"op"`
	UserID  string         `json:"user_id"`
	Session string         `json:"session"`
	Message *model.Message `json:"message,omitempty"`
	CallID  string         `json:"call_id,omitempty"`
	Reason  string         `json:"reason,omitempty"`
}

// dispatchReply 是 worker 的回复：先 claimed 表示已经接手，再 done 给出结果。
type dispatchReply struct {
	Kind  string     `json:"kind"`
	Run   string     `json:"run,omitempty"` // done：开始或继续的运行；为空表示没有运行，例如还有调用等待审批
	Error *wireError `json:"error,omitempty"`
}

// wireError 是跨实例传递的错误。errorx 错误按错误码还原，errors.Is 和 HTTP 状态码不变。
type wireError struct {
	Code    string         `json:"code,omitempty"`
	Kind    int            `json:"kind,omitempty"`
	Public  map[string]any `json:"public,omitempty"`
	Message string         `json:"message"`
}

func encodeError(err error) *wireError {
	if err == nil {
		return nil
	}
	w := &wireError{Message: err.Error()}
	if code := errorx.CodeOf(err); code != "" {
		w.Code, w.Kind, w.Public = code, int(errorx.KindOf(err)), errorx.PublicOf(err)
	}
	return w
}

func (w *wireError) decode() error {
	if w.Code != "" {
		var opts []errorx.Option
		for k, v := range w.Public {
			opts = append(opts, errorx.Public(k, v))
		}
		return errorx.Define(errorx.Kind(w.Kind), w.Code).New(opts...)
	}
	for _, known := range []error{ErrFinished, ErrNeedsHistory, ErrNeedsSessions, ErrRunOptions} {
		if w.Message == known.Error() {
			return known
		}
	}
	return errors.New(w.Message)
}

func (c *cluster) dispatchTopic() string       { return "ai.dispatch." + c.appID() }
func replyTopic(req string) string             { return "ai.dispatch.reply." + req }
func (c *cluster) claimLock(req string) string { return "ai:dispatch:" + req }

// dispatch 把请求转交给 worker，等它接手并给出结果，返回运行的镜像。
func (rs *Runs) dispatch(ctx context.Context, req dispatchReq, opts []agent.RunOption) (*Run, error) {
	if len(opts) > 0 {
		return nil, ErrRunOptions
	}
	c := rs.cluster
	req.ID = uuid.NewString()
	sub := c.hub.Subscribe(replyTopic(req.ID))
	defer sub.Close()
	if err := c.publish(c.dispatchTopic(), req); err != nil {
		return nil, err
	}
	wait := time.NewTimer(replyTimeout)
	defer wait.Stop()
	claimed := false
	for {
		select {
		case msg, ok := <-sub.C():
			if !ok {
				return nil, errors.Wrap(sub.Err(), "chat: dispatch")
			}
			var reply dispatchReply
			if err := json.Unmarshal(msg.Data, &reply); err != nil {
				continue
			}
			if reply.Kind == "claimed" && !claimed {
				claimed = true
				wait.Reset(dispatchTimeout)
				continue
			}
			if reply.Kind != "done" {
				continue
			}
			if reply.Error != nil {
				return nil, reply.Error.decode()
			}
			if reply.Run == "" {
				return nil, nil
			}
			return rs.getRun(ctx, req.UserID, req.Session, reply.Run)
		case <-wait.C:
			if !claimed {
				return nil, ErrNoWorker.New()
			}
			return nil, errors.Newf("chat: %s: the worker did not finish in %s", req.Op, dispatchTimeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Serve 接收 RunOnWorkers 实例转交的请求，在本实例开始运行，直到 ctx 取消后返回 nil。
// 多个实例同时 Serve 时，每个请求只由其中一个处理。Shutdown 开始后不再接收请求，由其他实例处理；
// 进行中的运行在 Shutdown 中等待结束。需要 Cluster。
func (rs *Runs) Serve(ctx context.Context) error {
	if rs.cluster == nil {
		return errors.New("chat: Serve needs Cluster")
	}
	c := rs.cluster
	for {
		sub := c.hub.Subscribe(c.dispatchTopic())
		for done := false; !done; {
			select {
			case <-ctx.Done():
				sub.Close()
				return nil
			case msg, ok := <-sub.C():
				if !ok {
					if errors.Is(sub.Err(), broadcast.ErrClosed) {
						return nil // Hub 已关闭
					}
					done = true // 订阅因为跟不上或后端丢消息而结束：重新订阅，请求方会超时后重试
					break
				}
				var req dispatchReq
				if err := json.Unmarshal(msg.Data, &req); err != nil || req.ID == "" {
					continue
				}
				go rs.serveOne(req)
			}
		}
		sub.Close()
	}
}

// serveOne 接手并处理一个请求。其他实例已经接手，或者本实例正在停止时什么也不做。
func (rs *Runs) serveOne(req dispatchReq) {
	rs.mu.Lock()
	draining := rs.draining
	rs.mu.Unlock()
	if draining {
		return
	}
	c := rs.cluster
	ctx, cancel := context.WithTimeout(context.Background(), dispatchTimeout)
	defer cancel()
	if _, err := c.locker.Acquire(ctx, c.claimLock(req.ID), dispatchTimeout); err != nil {
		return // 其他实例已经接手，或者锁存储出错：请求方会超时
	}
	reply := func(r dispatchReply) {
		if err := c.publish(replyTopic(req.ID), r); err != nil {
			rs.logger.Warn("reply to dispatched request", "op", req.Op, "error", err)
		}
	}
	reply(dispatchReply{Kind: "claimed"})

	var run *Run
	var err error
	switch req.Op {
	case opStart:
		if req.Message == nil {
			err = ErrBadRequest.New()
			break
		}
		run, err = rs.startLocal(ctx, req.UserID, req.Session, *req.Message, nil)
	case opNotify:
		run, err = rs.notifyLocal(ctx, req.UserID, req.Session)
	case opApprove:
		run, err = rs.approveLocal(ctx, req.UserID, req.Session, req.CallID, nil)
	case opReject:
		run, err = rs.rejectLocal(ctx, req.UserID, req.Session, req.CallID, req.Reason, nil)
	default:
		err = errors.Newf("chat: unknown dispatched operation %q", req.Op)
	}
	out := dispatchReply{Kind: "done", Error: encodeError(err)}
	if run != nil {
		out.Run = run.ID
		if beforeDone != nil {
			beforeDone(run)
		}
	}
	reply(out)
}

// getRun 返回对话中 ID 为 runID 的运行的镜像。本实例可能还保留着上一次运行的镜像，短时间内重试。
func (rs *Runs) getRun(ctx context.Context, userID, sessionID, runID string) (*Run, error) {
	key := rs.key(userID, sessionID)
	deadline := time.Now().Add(replyTimeout)
	for {
		rs.mu.Lock()
		r := rs.runs[key]
		rs.mu.Unlock()
		if r == nil || r.ID != runID {
			var err error
			if r, err = rs.cluster.mirror(ctx, rs, key, runID); err != nil {
				return nil, err
			}
		}
		if r != nil && r.ID == runID {
			return r, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.Newf("chat: cannot reach run %s on the worker", runID)
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
