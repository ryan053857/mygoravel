package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/logging"
)

var (
	// ErrBusy 表示对话正在运行，新消息应当用 Run.Steer 插入。
	ErrBusy = errorx.Define(errorx.Conflict, "ai.busy")
	// ErrAwaitingApproval 表示对话中有等待审批的工具调用，处理它们之前不能发送新消息。
	ErrAwaitingApproval = errorx.Define(errorx.Conflict, "ai.awaiting_approval")
	// ErrNotPending 表示要审批的工具调用不存在或已经处理过。
	ErrNotPending = errorx.Define(errorx.NotFound, "ai.not_pending")
	// ErrFinished 表示运行已经结束，不能再插入消息，应当开始新的运行。
	ErrFinished = errors.New("chat: run finished")
	// ErrShuttingDown 表示本实例正在停止，不再开始新的运行（503）。客户端稍后重试，
	// 多实例部署时会落到其他实例上。
	ErrShuttingDown = errorx.Define(errorx.Unavailable, "ai.shutting_down")
)

// steerExtension 是 trpc-agent-go 为已消费的插话消息事件附加的扩展字段。
const steerExtension = "trpc_agent.steer.queued_user_message"

// Runs 在后台运行一个 agent，并记录每个对话当前的运行。
//
// 同一个对话同时只有一个运行；运行结束后保留一段时间（默认 1 分钟），让断线的客户端接上剩余的事件。
type Runs struct {
	app       string
	runner    runner.Runner
	sessions  session.Service // 对话存储；使用 UseHistory 时为 nil
	history   HistoryStore
	ephemeral session.Service              // 使用 UseHistory 时，每一轮运行的临时会话
	approval  map[string]tool.CallableTool // 需要审批的工具；RequireApprovalOf 只登记名称，值为 nil
	choose    func(ctx context.Context, c Conversation) (agent.Agent, error)
	keep      time.Duration
	logger    *slog.Logger

	runnerOpts []runner.Option
	cluster    *cluster // 多实例协作，见 Cluster；nil 表示单实例
	workers    bool     // RunOnWorkers：开始运行的请求转交给调用 Serve 的实例

	mu        sync.Mutex
	runs      map[session.Key]*Run
	appending map[session.Key]bool // Append 正在写入的对话，期间不开始运行
	wg        sync.WaitGroup
	draining  bool // Shutdown 或 Close 开始后不再开始新的运行
}

// Option 配置 Runs。
type Option func(*Runs)

// RequireApproval 让模型调用这些工具时不执行，运行停下等待 Runs.Approve 或 Runs.Reject。
// 工具同样要加入 agent 的工具列表，模型才能调用它们。
func RequireApproval(tools ...tool.CallableTool) Option {
	return func(rs *Runs) {
		for _, t := range tools {
			rs.approval[t.Declaration().Name] = t
		}
	}
}

// RequireApprovalOf 按名称要求审批，用于工具由 AgentFor 按对话提供、创建 Runs 时还不存在的情况。
// 批准后执行这个对话当前的 agent 上同名的工具。
func RequireApprovalOf(names ...string) Option {
	return func(rs *Runs) {
		for _, name := range names {
			if _, ok := rs.approval[name]; !ok {
				rs.approval[name] = nil
			}
		}
	}
}

// AgentFor 按对话选择运行的 agent，例如按会话所属的 AI 员工取出它的模型、指令和工具。
// 每一轮请求模型之前调用，配置的修改在下一轮生效；这时 NewRuns 的 ag 传 nil。
// choose 返回错误时这一轮失败，运行以 error 事件结束。创建 llmagent 的开销很小，也可以由应用按配置缓存。
func AgentFor(choose func(ctx context.Context, c Conversation) (agent.Agent, error)) Option {
	if choose == nil {
		panic("chat: nil agent chooser")
	}
	return func(rs *Runs) { rs.choose = choose }
}

// KeepFinished 设置运行结束后保留的时间，期间重连的客户端仍能取得剩余的事件。
func KeepFinished(d time.Duration) Option { return func(rs *Runs) { rs.keep = d } }

// WithLogger 设置记录运行错误的日志，默认使用 slog.Default()。
func WithLogger(l *slog.Logger) Option { return func(rs *Runs) { rs.logger = l } }

// RunnerOptions 把选项传给 runner.NewRunner，例如 runner.WithMemoryService。
func RunnerOptions(opts ...runner.Option) Option {
	return func(rs *Runs) { rs.runnerOpts = append(rs.runnerOpts, opts...) }
}

// NewRuns 为 agent 创建 Runs，对话保存在 sessions 中。app 是应用名，用于区分同一个存储中不同应用的对话。
// 使用 UseHistory 时对话保存在应用自己的存储中，sessions 传 nil。两者都没有或者同时提供时 panic。
// 使用 AgentFor 按对话选择 agent 时 ag 传 nil；两者都没有或者同时提供时 panic。
func NewRuns(app string, ag agent.Agent, sessions session.Service, opts ...Option) *Runs {
	rs := &Runs{
		app:      app,
		sessions: sessions,
		approval: map[string]tool.CallableTool{},
		keep:     time.Minute,
		runs:     map[session.Key]*Run{},

		appending: map[session.Key]bool{},
	}
	for _, opt := range opts {
		opt(rs)
	}
	switch {
	case rs.history == nil && sessions == nil:
		panic("chat: NewRuns needs a session service or UseHistory")
	case rs.history != nil && sessions != nil:
		panic("chat: pass nil sessions with UseHistory")
	case rs.history != nil:
		rs.ephemeral = inmemory.NewSessionService()
		sessions = rs.ephemeral
	}
	if rs.workers && rs.cluster == nil {
		panic("chat: RunOnWorkers needs Cluster")
	}
	switch {
	case ag == nil && rs.choose == nil:
		panic("chat: NewRuns needs an agent or AgentFor")
	case ag != nil && rs.choose != nil:
		panic("chat: pass a nil agent with AgentFor")
	}
	rs.logger = logging.Or(rs.logger).With(logging.AttrComponent, "ai")
	runnerOpts := append([]runner.Option{runner.WithSessionService(sessions)}, rs.runnerOpts...)
	if ag != nil {
		rs.runner = runner.NewRunner(app, ag, runnerOpts...)
	} else {
		// 每一轮用 agent.WithAgent 指定 AgentFor 选出的 agent，默认的 agent 不会被用到。
		rs.runner = runner.NewRunnerWithAgentFactory(app, app, func(context.Context, agent.RunOptions) (agent.Agent, error) {
			return nil, errors.New("chat: no agent chosen for the run")
		}, runnerOpts...)
	}
	return rs
}

// Shutdown 平滑停止本实例上的运行：不再开始新的运行（Start、Submit、Approve、Reject 返回 ErrShuttingDown），
// 进行中的运行继续直到结束；ctx 结束时仍未结束的运行被取消，已经生成的内容保留在对话中。
// 进行中的运行可以照常插话；多实例部署时，其他实例在此期间仍能镜像和转发它们。
//
// 在 HTTP 服务停止之后、Close 之前调用。ctx 结束后取消的运行记录一条 warn 日志，不作为错误返回。
func (rs *Runs) Shutdown(ctx context.Context) error {
	rs.mu.Lock()
	rs.draining = true
	rs.mu.Unlock()

	done := make(chan struct{})
	go func() {
		rs.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	rs.mu.Lock()
	canceled := 0
	for _, r := range rs.runs {
		if r.remote == nil && !r.isFinished() {
			r.cancel()
			canceled++
		}
	}
	rs.mu.Unlock()
	<-done
	if canceled > 0 {
		rs.logger.Warn("canceled unfinished runs on shutdown", "runs", canceled)
	}
	return nil
}

// Close 取消本实例上的所有运行并等待它们结束，然后关闭 runner。会话存储由调用方关闭。
// 需要等进行中的运行结束时，先调用 Shutdown。
func (rs *Runs) Close() error {
	rs.mu.Lock()
	rs.draining = true
	for _, r := range rs.runs {
		if r.remote != nil {
			r.remote.close()
		} else {
			r.cancel()
		}
	}
	rs.mu.Unlock()
	rs.wg.Wait()
	rs.mu.Lock()
	for _, r := range rs.runs {
		if r.own != nil {
			r.own.close()
		}
	}
	rs.mu.Unlock()
	err := rs.runner.Close()
	if rs.ephemeral != nil {
		err = errors.Join(err, rs.ephemeral.Close())
	}
	return err
}

func (rs *Runs) key(userID, sessionID string) session.Key {
	return session.Key{AppName: rs.app, UserID: userID, SessionID: sessionID}
}

// Send 把一条用户消息交给对话：对话没有在运行时开始一次运行，正在运行时插入这次运行（见 Run.Steer）。
// 返回处理这条消息的运行。对话在等待审批时返回 ErrAwaitingApproval。
func (rs *Runs) Send(ctx context.Context, userID, sessionID string, msg model.Message, opts ...agent.RunOption) (*Run, error) {
	for {
		r, err := rs.Start(ctx, userID, sessionID, msg, opts...)
		if !errors.Is(err, ErrBusy) {
			return r, err
		}
		r, err = rs.Get(ctx, userID, sessionID)
		if err != nil {
			return nil, err
		}
		if r != nil {
			err := r.Steer(ctx, msg)
			if err == nil {
				return r, nil
			}
			if !errors.Is(err, ErrFinished) {
				return nil, err
			}
		}
		// 运行恰好结束：重新开始。
	}
}

// Start 在后台开始一次运行，把 msg 发给 agent。对话不存在时自动创建。
//
// 运行不受 ctx 取消的影响，但保留其中的值（例如日志字段）。对话正在运行时返回 ErrBusy，
// 等待审批时返回 ErrAwaitingApproval。
func (rs *Runs) Start(ctx context.Context, userID, sessionID string, msg model.Message, opts ...agent.RunOption) (*Run, error) {
	if rs.workers {
		return rs.dispatch(ctx, dispatchReq{Op: opStart, UserID: userID, Session: sessionID, Message: &msg}, opts)
	}
	return rs.startLocal(ctx, userID, sessionID, msg, opts)
}

func (rs *Runs) startLocal(ctx context.Context, userID, sessionID string, msg model.Message, opts []agent.RunOption) (*Run, error) {
	key := rs.key(userID, sessionID)
	if rs.localBusy(key) {
		return nil, ErrBusy.New()
	}
	if len(rs.approval) > 0 {
		_, pending, err := rs.pending(ctx, key)
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			return nil, ErrAwaitingApproval.New()
		}
	}
	return rs.start(ctx, key, &msg, opts, nil)
}

// start 开始运行。results 非空表示审批之后继续：运行先发出这些调用结果，
// 上一次运行停下时还没送达的插话消息随之送达。msg 为 nil 表示使用 UseHistory 时由 Load 读出全部输入。
func (rs *Runs) start(ctx context.Context, key session.Key, msg *model.Message, opts []agent.RunOption, results []Event) (*Run, error) {
	own, err := rs.own(ctx, key)
	if err != nil {
		return nil, err
	}
	return rs.startOwned(ctx, key, own, msg, opts, results)
}

// own 检查本实例上没有正在运行的对话；多实例时还取得对话的锁，其他实例上正在运行时返回 ErrBusy。
// 单实例时返回的 owner 为 nil。
func (rs *Runs) own(ctx context.Context, key session.Key) (*owner, error) {
	if rs.localBusy(key) {
		return nil, ErrBusy.New()
	}
	if rs.cluster == nil {
		return nil, nil
	}
	return rs.cluster.acquire(ctx, key)
}

// startOwned 与 start 相同，own 是已经取得的对话的锁（单实例时为 nil），出错时释放它。
func (rs *Runs) startOwned(ctx context.Context, key session.Key, own *owner, msg *model.Message, opts []agent.RunOption, results []Event) (_ *Run, err error) {
	if own != nil {
		defer func() {
			if err != nil {
				own.release()
			}
		}()
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.draining {
		return nil, ErrShuttingDown.New()
	}
	if rs.appending[key] {
		return nil, ErrBusy.New() // Append 正在写入这个对话
	}
	prev, ok := rs.runs[key]
	if ok && prev.remote == nil && !prev.isFinished() {
		return nil, ErrBusy.New()
	}
	if ok && prev.remote != nil {
		// 取得了锁，说明镜像的那次运行已经结束。
		prev.remote.close()
		prev = nil
	}
	if ok && prev != nil && prev.own != nil {
		// 上一次运行被这次取代，它的 owner 不再回应控制请求；否则它会一直订阅控制主题。
		prev.own.close()
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r := &Run{
		ID:      uuid.NewString(),
		key:     key,
		rs:      rs,
		cancel:  cancel,
		changed: make(chan struct{}),
		done:    make(chan struct{}),
	}
	for _, ev := range results {
		ev.ID = fmt.Sprintf("%s.%d", r.ID, len(r.events))
		r.events = append(r.events, ev)
	}
	if results != nil && prev != nil {
		prev.mu.Lock()
		r.steered, prev.steered = prev.steered, nil
		prev.mu.Unlock()
	}
	if len(rs.approval) > 0 {
		names := make([]string, 0, len(rs.approval))
		for name := range rs.approval {
			names = append(names, name)
		}
		opts = append(opts, agent.WithToolExecutionFilter(tool.NewExcludeToolNamesFilter(names...)))
	}
	rs.runs[key] = r
	if own != nil {
		own.serve(r)
	}
	rs.wg.Add(1)
	go func() {
		defer rs.wg.Done()
		r.loop(runCtx, msg, opts)
		cancel()
		if own != nil {
			own.finish()
		}
		time.AfterFunc(rs.keep, func() {
			rs.mu.Lock()
			// 停下等待审批时还没送达的插话消息保存在运行中，审批之后送达，所以这样的运行不删除。
			removed := rs.runs[key] == r && !r.hasSteered()
			if removed {
				delete(rs.runs, key)
			}
			rs.mu.Unlock()
			if removed && own != nil {
				own.close()
			}
		})
	}()
	return r, nil
}

// localBusy 报告本实例上是否有这个对话正在进行的运行。
func (rs *Runs) localBusy(key session.Key) bool {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	r, ok := rs.runs[key]
	return rs.appending[key] || (ok && r.remote == nil && !r.isFinished())
}

// Get 返回对话当前的运行，或者刚结束、仍在保留期内的运行；没有时返回 nil。
//
// 配置了 Cluster 时，运行在其他实例上也能取得：返回的 Run 是它的镜像，事件经由广播送达，
// Steer 和 Cancel 转发给运行所在的实例。
func (rs *Runs) Get(ctx context.Context, userID, sessionID string) (*Run, error) {
	key := rs.key(userID, sessionID)
	rs.mu.Lock()
	r, ok := rs.runs[key]
	rs.mu.Unlock()
	if ok && (r.remote == nil || !r.isFinished()) {
		return r, nil
	}
	if rs.cluster == nil {
		return r, nil
	}
	return rs.cluster.mirror(ctx, rs, key, "")
}

// Steer 把消息插入对话正在进行的运行，见 Run.Steer。对话没有在运行时返回 ErrFinished。
func (rs *Runs) Steer(ctx context.Context, userID, sessionID string, msg model.Message) error {
	r, err := rs.Get(ctx, userID, sessionID)
	if err != nil {
		return err
	}
	if r == nil {
		return ErrFinished
	}
	return r.Steer(ctx, msg)
}

// Pending 从会话存储读出对话中等待审批的工具调用，不依赖运行所在的进程。
func (rs *Runs) Pending(ctx context.Context, userID, sessionID string) ([]ToolCall, error) {
	_, pending, err := rs.pending(ctx, rs.key(userID, sessionID))
	if err != nil {
		return nil, err
	}
	calls := make([]ToolCall, len(pending))
	for i, p := range pending {
		calls[i] = p.call
	}
	return calls, nil
}

// Approve 执行一个等待审批的工具调用，把结果写回对话。
// 这是最后一个等待审批的调用时，对话在后台继续运行并返回该运行；否则返回 nil，等待其余调用的决定。
func (rs *Runs) Approve(ctx context.Context, userID, sessionID, callID string, opts ...agent.RunOption) (*Run, error) {
	if rs.workers {
		return rs.dispatch(ctx, dispatchReq{Op: opApprove, UserID: userID, Session: sessionID, CallID: callID}, opts)
	}
	return rs.approveLocal(ctx, userID, sessionID, callID, opts)
}

func (rs *Runs) approveLocal(ctx context.Context, userID, sessionID, callID string, opts []agent.RunOption) (*Run, error) {
	key := rs.key(userID, sessionID)
	return rs.resolve(ctx, key, callID, opts, func(call ToolCall) string {
		t, err := rs.approvalTool(ctx, key, call.Name)
		if err != nil {
			return "Error: " + err.Error()
		}
		out, err := t.Call(ctx, []byte(call.Arguments))
		if err != nil {
			return "Error: " + err.Error()
		}
		if s, ok := out.(string); ok {
			return s
		}
		b, err := json.Marshal(out)
		if err != nil {
			return "Error: " + err.Error()
		}
		return string(b)
	})
}

// Reject 拒绝一个等待审批的工具调用，把 reason 作为调用结果告诉模型，其余同 Approve。
func (rs *Runs) Reject(ctx context.Context, userID, sessionID, callID, reason string, opts ...agent.RunOption) (*Run, error) {
	if rs.workers {
		return rs.dispatch(ctx, dispatchReq{Op: opReject, UserID: userID, Session: sessionID, CallID: callID, Reason: reason}, opts)
	}
	return rs.rejectLocal(ctx, userID, sessionID, callID, reason, opts)
}

func (rs *Runs) rejectLocal(ctx context.Context, userID, sessionID, callID, reason string, opts []agent.RunOption) (*Run, error) {
	return rs.resolve(ctx, rs.key(userID, sessionID), callID, opts, func(ToolCall) string {
		return rejection(reason)
	})
}

func (rs *Runs) resolve(ctx context.Context, key session.Key, callID string, opts []agent.RunOption, result func(ToolCall) string) (*Run, error) {
	// 先取得对话的锁再执行批准的工具：对话正忙时返回 ErrBusy，工具不会执行，重试也不会执行两次。
	own, err := rs.own(ctx, key)
	if err != nil {
		return nil, err
	}
	started := false
	defer func() {
		if !started && own != nil {
			own.release()
		}
	}()
	t, pending, err := rs.pending(ctx, key)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, p := range pending {
		if p.call.ID == callID {
			idx = i
		}
	}
	if idx < 0 {
		return nil, ErrNotPending.New(errorx.Public("call_id", callID))
	}
	p := pending[idx]
	content := result(p.call)
	msg := model.NewToolMessage(p.call.ID, p.call.Name, content)
	if len(pending) > 1 {
		// 还有其他调用等待决定：结果先写进对话，最后一个决定到来时再继续运行。
		if rs.history != nil {
			return nil, rs.save(ctx, key, Entry{Message: msg})
		}
		evt := event.NewResponseEvent(p.source.InvocationID, p.source.Author, &model.Response{
			Object:  model.ObjectTypeToolResponse,
			Done:    true,
			Choices: []model.Choice{{Message: msg}},
		})
		evt.Branch, evt.FilterKey = p.source.Branch, p.source.FilterKey
		return nil, rs.sessions.AppendEvent(ctx, t.sess, evt)
	}
	// 继续的运行先发出这一批调用的结果，客户端据此更新审批状态。
	var results []Event
	answers := toolAnswers(t)
	for _, c := range p.batch {
		text, ok := answers[c.ID]
		if c.ID == p.call.ID {
			text, ok = content, true
		}
		if ok {
			results = append(results, Event{Name: EventToolResult, Data: newToolResult(c.ID, c.Name, text)})
		}
	}
	started = true // startOwned 出错时自己释放锁
	return rs.startOwned(ctx, key, own, &msg, opts, results)
}

// approvalTool 返回批准后执行的工具：RequireApproval 登记的工具；使用 AgentFor 时，
// 优先取这个对话当前的 agent 上同名的工具。
func (rs *Runs) approvalTool(ctx context.Context, key session.Key, name string) (tool.CallableTool, error) {
	if rs.choose != nil {
		ag, err := rs.choose(ctx, rs.conversation(key))
		if err != nil {
			return nil, err
		}
		for _, t := range ag.Tools() {
			if c, ok := t.(tool.CallableTool); ok && t.Declaration().Name == name {
				return c, nil
			}
		}
	}
	if t := rs.approval[name]; t != nil {
		return t, nil
	}
	return nil, errors.Newf("chat: tool %q is not available", name)
}

// rejection 是 Reject 写给模型的调用结果。
func rejection(reason string) string {
	b, _ := json.Marshal(map[string]string{"approval": "rejected", "reason": reason})
	return string(b)
}

func newToolResult(id, name, content string) ToolResult {
	var r struct{ Approval, Reason string }
	denied := json.Unmarshal([]byte(content), &r) == nil && r.Approval == "rejected"
	return ToolResult{ID: id, Name: name, Content: content, Denied: denied}
}

// toolAnswers 返回对话中各工具调用的结果。
func toolAnswers(t transcript) map[string]string {
	out := map[string]string{}
	t.each(func(_ *event.Event, m model.Message) {
		if m.Role == model.RoleTool {
			out[m.ToolID] = m.Content
		}
	})
	return out
}

type pendingCall struct {
	call   ToolCall
	source *event.Event // 调用所在的会话事件；使用 UseHistory 时为 nil
	batch  []ToolCall   // 同一次模型回复中需要审批的全部调用
}

// pending 找出对话中最后一次调用里需要审批、还没有结果的工具调用。
func (rs *Runs) pending(ctx context.Context, key session.Key) (transcript, []pendingCall, error) {
	t, err := rs.load(ctx, key)
	if err != nil {
		return t, nil, err
	}
	var calls []pendingCall
	answered := map[string]bool{}
	t.each(func(ev *event.Event, m model.Message) {
		if m.Role == model.RoleTool {
			answered[m.ToolID] = true
		}
		if len(m.ToolCalls) > 0 {
			calls = nil
			var batch []ToolCall
			for _, tc := range m.ToolCalls {
				if _, ok := rs.approval[tc.Function.Name]; ok {
					batch = append(batch, toolCall(tc))
				}
			}
			for _, call := range batch {
				calls = append(calls, pendingCall{call, ev, batch})
			}
		}
	})
	var out []pendingCall
	for _, c := range calls {
		if !answered[c.call.ID] {
			out = append(out, c)
		}
	}
	return t, out, nil
}

func toolCall(tc model.ToolCall) ToolCall {
	return ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: string(tc.Function.Arguments)}
}

// Run 是对话的一次后台运行。它的方法可以并发调用。
type Run struct {
	// ID 唯一标识这次运行。
	ID string

	key    session.Key
	rs     *Runs
	cancel context.CancelFunc
	remote *mirror // 非 nil 表示这是其他实例上的运行的镜像
	own    *owner  // 配置了 Cluster 时，本实例上的运行在集群中的一面

	mu       sync.Mutex
	events   []Event
	changed  chan struct{} // 追加事件时关闭并替换，唤醒等待的读者
	done     chan struct{}
	finished bool
	reqID    string          // 当前 trpc-agent-go 请求的 ID
	steered  []model.Message // 已插入、还没有被 agent 消费的消息
	notified bool            // Notify 在这一轮读取历史之后到达，结束后再运行一轮
}

// Steer 在运行中途插入一条用户消息。它在当前这批工具调用结束后、下一次请求模型之前进入对话；
// 如果模型已经在给出最终回答，回答结束后运行会带着这条消息继续。运行已经结束时返回 ErrFinished。
func (r *Run) Steer(ctx context.Context, msg model.Message) error {
	if r.remote != nil {
		return r.remote.steer(ctx, msg)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return ErrFinished
	}
	r.steered = append(r.steered, msg)
	if r.reqID != "" {
		// 失败（例如请求恰好结束）时消息留在 steered 中，由 loop 在下一轮发出。
		_ = runner.EnqueueUserMessage(r.rs.runner, r.reqID, msg)
	}
	return nil
}

// Cancel 取消运行。已经生成的消息保留在对话中。
func (r *Run) Cancel() { r.cancel() }

// Done 在运行结束、最后一个事件（done）产生后关闭。
func (r *Run) Done() <-chan struct{} { return r.done }

func (r *Run) hasSteered() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.steered) > 0
}

func (r *Run) isFinished() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finished
}

// Events 依次返回 lastEventID 之后的事件，然后等待新的事件，直到运行结束或 ctx 取消。
// lastEventID 为空或者不属于这次运行时，从第一个事件开始。
func (r *Run) Events(ctx context.Context, lastEventID string) func(yield func(Event) bool) {
	next := 0
	if lastEventID != "" {
		r.mu.Lock()
		for i := len(r.events) - 1; i >= 0; i-- {
			if r.events[i].ID == lastEventID {
				next = i + 1
				break
			}
		}
		r.mu.Unlock()
	}
	return func(yield func(Event) bool) {
		for {
			r.mu.Lock()
			if next < len(r.events) {
				ev := r.events[next]
				r.mu.Unlock()
				next++
				if !yield(ev) {
					return
				}
				continue
			}
			finished, changed := r.finished, r.changed
			r.mu.Unlock()
			if finished {
				return
			}
			select {
			case <-changed:
			case <-ctx.Done():
				return
			}
		}
	}
}

func (r *Run) emit(name string, data any, raw *event.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, Event{ID: fmt.Sprintf("%s.%d", r.ID, len(r.events)), Name: name, Data: data, Raw: raw})
	close(r.changed)
	r.changed = make(chan struct{})
}

// loop 运行 agent，直到没有剩余的插话消息和通知。msg 为 nil 时，输入全部由 UseHistory 的 Load 读出。
func (r *Run) loop(ctx context.Context, msg *model.Message, opts []agent.RunOption) {
	defer close(r.done)
	var failed bool
	calls := map[string]ToolCall{}
	answered := map[string]bool{}
	for first := true; ; first = false {
		reqID := uuid.NewString()
		r.mu.Lock()
		r.reqID = reqID
		r.notified = false // 这一轮读取的历史包括此前通知的消息
		queued := append([]model.Message(nil), r.steered...)
		r.mu.Unlock()
		if !first && msg != nil {
			r.emit(EventUser, Text{msg.Content}, nil)
		}
		ch, cursor, cleanup, err := r.round(ctx, msg, reqID, opts)
		switch {
		case err != nil:
			r.fail(ctx, err)
			failed = true
		case ch != nil:
			for _, m := range queued {
				_ = runner.EnqueueUserMessage(r.rs.runner, reqID, m)
			}
			failed = r.consume(ctx, ch, cursor, calls, answered)
		}
		cleanup()

		r.mu.Lock()
		r.reqID = ""
		if !failed && ctx.Err() == nil && len(r.pendingCalls(calls, answered)) == 0 {
			if len(r.steered) > 0 {
				next := r.steered[0]
				msg = &next
				r.steered = r.steered[1:]
				r.mu.Unlock()
				continue
			}
			if r.notified {
				msg = nil
				r.mu.Unlock()
				continue
			}
		}
		done := Done{Pending: r.pendingCalls(calls, answered), Canceled: ctx.Err() != nil}
		if done.Pending == nil {
			done.Pending = []ToolCall{}
		}
		r.events = append(r.events, Event{ID: fmt.Sprintf("%s.%d", r.ID, len(r.events)), Name: EventDone, Data: done})
		r.finished = true
		close(r.changed)
		r.changed = make(chan struct{})
		r.mu.Unlock()
		return
	}
}

// round 开始一轮 agent 运行，返回它的事件、这一轮读取历史的游标和结束后的清理函数。
//
// 默认在对话的会话中运行 msg。使用 UseHistory 时先保存 msg，再用 Load 读出的历史填充一个只在这一轮使用的会话，
// 最后一条作为输入；没有需要回应的消息（历史为空或最后一条是模型的回答）时 ch 为 nil。
func (r *Run) round(ctx context.Context, msg *model.Message, reqID string, opts []agent.RunOption) (_ <-chan *event.Event, cursor string, cleanup func(), _ error) {
	rs := r.rs
	cleanup = func() {}
	opts = append(opts, agent.WithRequestID(reqID))
	if rs.history != nil && msg != nil {
		// 先保存输入：选择 agent 失败时，用户的消息也不会丢失。
		if err := rs.save(ctx, r.key, Entry{Message: *msg, RunID: r.ID}); err != nil {
			return nil, "", cleanup, err
		}
	}
	if rs.choose != nil {
		ag, err := rs.choose(ctx, rs.conversation(r.key))
		if err != nil {
			return nil, "", cleanup, errors.Wrap(err, "chat: choose agent")
		}
		opts = append(opts, agent.WithAgent(ag))
	}
	if rs.history == nil {
		ch, err := rs.runner.Run(ctx, r.key.UserID, r.key.SessionID, *msg, opts...)
		return ch, "", cleanup, err
	}
	t, err := rs.load(ctx, r.key)
	if err != nil {
		return nil, "", cleanup, err
	}
	if len(t.msgs) == 0 || t.msgs[len(t.msgs)-1].Role == model.RoleAssistant {
		return nil, "", cleanup, nil
	}
	// 临时会话只在这一轮使用；trpc-agent-go 要求用户 ID 非空，共享的对话可能没有用户。
	tmp := session.Key{AppName: rs.app, UserID: cmp.Or(r.key.UserID, "-"), SessionID: r.ID + "." + reqID}
	cleanup = func() { _ = rs.ephemeral.DeleteSession(context.WithoutCancel(ctx), tmp) }
	ch, err := rs.runner.Run(ctx, tmp.UserID, tmp.SessionID, t.msgs[len(t.msgs)-1], append(opts, agent.WithMessages(t.msgs))...)
	return ch, t.cursor, cleanup, err
}

// pendingCalls 返回需要审批、还没有结果的调用。调用方持有 r.mu。
func (r *Run) pendingCalls(calls map[string]ToolCall, answered map[string]bool) []ToolCall {
	var out []ToolCall
	for id, c := range calls {
		if _, ok := r.rs.approval[c.Name]; ok && !answered[id] {
			out = append(out, c)
		}
	}
	return out
}

func (r *Run) fail(ctx context.Context, err error) {
	if ctx.Err() != nil {
		return
	}
	r.rs.logger.ErrorContext(ctx, "agent run failed", "session_id", r.key.SessionID, "error", err)
	r.emit(EventError, Error{err.Error()}, nil)
}

// consume 把 trpc-agent-go 的事件转换为 Event，返回运行是否出错。
// cursor 是这一轮读取历史的游标，使用 UseHistory 时随产生的消息交给 Save。
func (r *Run) consume(ctx context.Context, ch <-chan *event.Event, cursor string, calls map[string]ToolCall, answered map[string]bool) (failed bool) {
	// 当前这条回答的思考过程和正文是否已经以增量发出；已发出的部分不再从完整消息中重复发送。
	var streamedReasoning, streamedText bool
	// save 在使用 UseHistory 时保存进入对话的完整消息；保存失败时停止运行，之后的消息不再保存。
	var saveErr error
	save := func(m model.Message) {
		if saveErr != nil {
			return
		}
		if saveErr = r.rs.save(ctx, r.key, Entry{Message: m, RunID: r.ID, After: cursor}); saveErr != nil {
			failed = true
			r.rs.logger.ErrorContext(ctx, "agent run failed", "session_id", r.key.SessionID, "error", saveErr)
			r.emit(EventError, Error{saveErr.Error()}, nil)
			r.cancel()
		}
	}
	for ev := range ch {
		if ev == nil || ev.Response == nil {
			continue
		}
		if ev.Error != nil {
			if ctx.Err() == nil {
				failed = true
				r.rs.logger.ErrorContext(ctx, "agent run failed", "session_id", r.key.SessionID, "error", ev.Error.Message)
				r.emit(EventError, Error{ev.Error.Message}, ev)
			}
			continue
		}
		for _, c := range ev.Choices {
			if ev.IsPartial {
				if c.Delta.ReasoningContent != "" {
					streamedReasoning = true
					r.emit(EventReasoning, Text{c.Delta.ReasoningContent}, ev)
				}
				if c.Delta.Content != "" {
					streamedText = true
					r.emit(EventText, Text{c.Delta.Content}, ev)
				}
				continue
			}
			m := c.Message
			switch m.Role {
			case model.RoleUser:
				if _, ok := ev.Extensions[steerExtension]; ok {
					r.mu.Lock()
					if len(r.steered) > 0 {
						r.steered = r.steered[1:]
					}
					r.mu.Unlock()
					r.emit(EventUser, Text{m.Content}, ev)
					save(m)
				}
			case model.RoleTool:
				answered[m.ToolID] = true
				r.emit(EventToolResult, newToolResult(m.ToolID, m.ToolName, m.Content), ev)
				save(m)
			case model.RoleAssistant:
				if m.Content != "" || m.ReasoningContent != "" || len(m.ToolCalls) > 0 {
					save(m)
				}
				if !streamedReasoning && m.ReasoningContent != "" {
					r.emit(EventReasoning, Text{m.ReasoningContent}, ev)
				}
				if !streamedText && m.Content != "" {
					r.emit(EventText, Text{m.Content}, ev)
				}
				streamedReasoning, streamedText = false, false
				for _, tc := range m.ToolCalls {
					call := toolCall(tc)
					calls[call.ID] = call
					r.emit(EventToolCall, call, ev)
				}
			}
		}
	}
	return failed
}
