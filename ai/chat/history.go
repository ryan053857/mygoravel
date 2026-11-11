package chat

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

// HistoryStore 把对话的历史保存在应用自己的消息表中，见 UseHistory。
type HistoryStore interface {
	// Load 返回交给模型的上下文，以及标记它读到哪里的游标。
	//
	// 消息按逻辑顺序排列：Save 保存的每一条排在它的 Entry.After 所指的位置之后，而不是按写入时间。
	// 模型回答期间其他人发的消息因此排在回答之后，下一轮会接着回应它们。最后一条是这次运行要回应的消息：
	// 用户或其他参与者说的话，或者审批后工具调用的结果；最后一条是模型的回答时不运行。
	//
	// 返回哪些消息、怎样标明发言人（例如群聊中在内容前加上名字）、是否包括已编辑或删除的内容，都由应用决定。
	// 带工具调用的 assistant 消息和工具结果要原样返回：等待审批的调用由它们算出。
	Load(ctx context.Context, c Conversation) (History, error)

	// Save 保存进入对话的一条消息：Start、Submit 和插话发送的用户消息，模型的回答（可能带工具调用），
	// 工具调用的结果，以及 Approve、Reject 的结果。应用自己写入、再用 Notify 触发运行的消息不会交给 Save。
	Save(ctx context.Context, c Conversation, e Entry) error
}

// History 是 HistoryStore.Load 读出的上下文。
type History struct {
	Messages []model.Message
	// Cursor 标记读到哪里，由应用定义，例如最后一条消息的序号。这一轮产生的消息带着它交给 Save。
	Cursor string
}

// Entry 是交给 HistoryStore.Save 的一条消息。
type Entry struct {
	Message model.Message
	// RunID 是产生它的运行；Approve、Reject 在其他调用还没有决定时只写入结果，此时为空。
	RunID string
	// After 是这条消息基于的上下文：产生它的那一轮 Load 返回的 Cursor。Load 把它排在这个位置之后、
	// 同一位置的消息按保存的先后。为空表示它不基于读出的上下文（Start、Submit 发来的用户消息，Approve、
	// Reject 的结果），排在当前所有消息之后。
	After string
}

// Conversation 标识一个对话，即 Runs 的方法传入的 userID 和 sessionID。
// 多人共享的对话（例如群聊）没有单一的用户，UserID 由应用约定，例如空字符串或租户 ID。
type Conversation struct {
	UserID string
	ID     string
}

// UseHistory 让 Runs 从应用的 store 读写对话历史，不使用 trpc-agent-go 的会话存储，NewRuns 的 sessions 传 nil。
//
// 每一轮请求模型之前，用 store.Load 组装上下文，放进一个只在这一轮使用的内存会话；运行中产生的完整消息
// 逐条交给 store.Save。流式事件、插话、停止、审批和多实例协作的用法不变。
//
// 这时 UIMessages 和 Append 不可用：页面显示应用自己的消息，其他消息由应用直接写入，再调用 Notify。
func UseHistory(store HistoryStore) Option {
	if store == nil {
		panic("chat: nil history store")
	}
	return func(rs *Runs) { rs.history = store }
}

// ErrNeedsSessions 表示方法需要 trpc-agent-go 的会话存储，在使用 UseHistory 时不可用。
var ErrNeedsSessions = errors.New("chat: not available with UseHistory; the application stores the messages")

// ErrNeedsHistory 表示方法需要 UseHistory。
var ErrNeedsHistory = errors.New("chat: Notify needs UseHistory")

// Notify 告诉对话有新的消息需要回应，只能与 UseHistory 一起使用：应用先把消息写入自己的存储，再调用它。
//
// 对话没有在运行时开始一次运行，上下文由 Load 读出。正在运行时（在任何一个实例上）记下通知，
// 这次运行结束后再运行一轮，期间写入的消息都在新一轮的上下文中；多次通知只多运行一轮。
// 返回处理通知的运行。有等待审批的工具调用时返回 ErrAwaitingApproval。
func (rs *Runs) Notify(ctx context.Context, userID, sessionID string) (*Run, error) {
	if rs.history == nil {
		return nil, ErrNeedsHistory
	}
	if rs.workers {
		return rs.dispatch(ctx, dispatchReq{Op: opNotify, UserID: userID, Session: sessionID}, nil)
	}
	return rs.notifyLocal(ctx, userID, sessionID)
}

func (rs *Runs) notifyLocal(ctx context.Context, userID, sessionID string) (*Run, error) {
	key := rs.key(userID, sessionID)
	for {
		if len(rs.approval) > 0 {
			_, pending, err := rs.pending(ctx, key)
			if err != nil {
				return nil, err
			}
			if len(pending) > 0 {
				return nil, ErrAwaitingApproval.New()
			}
		}
		r, err := rs.start(ctx, key, nil, nil, nil)
		if !errors.Is(err, ErrBusy) {
			return r, err
		}
		r, err = rs.Get(ctx, userID, sessionID)
		if err != nil {
			return nil, err
		}
		if r != nil {
			err := r.notify(ctx)
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

// notify 记下对话有新的消息，这次运行结束后再运行一轮。运行已经结束时返回 ErrFinished。
func (r *Run) notify(ctx context.Context) error {
	if r.remote != nil {
		return r.remote.request(ctx, ctlMsg{Kind: "notify"})
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return ErrFinished
	}
	r.notified = true
	return nil
}

func (rs *Runs) conversation(key session.Key) Conversation {
	return Conversation{UserID: key.UserID, ID: key.SessionID}
}

// saveTimeout 是 Save 单次调用的超时。它不随运行取消：停止前已经产生的消息仍要保存。
const saveTimeout = 10 * time.Second

// save 在使用 UseHistory 时把进入对话的消息交给应用保存。
func (rs *Runs) save(ctx context.Context, key session.Key, e Entry) error {
	if rs.history == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), saveTimeout)
	defer cancel()
	return errors.Wrap(rs.history.Save(ctx, rs.conversation(key), e), "chat: save message")
}

// transcript 是对话的历史：默认来自 trpc-agent-go 的会话，使用 UseHistory 时来自 store.Load。
type transcript struct {
	sess   *session.Session
	msgs   []model.Message
	cursor string
}

// load 读取对话的历史。对话不存在时返回空的历史。
func (rs *Runs) load(ctx context.Context, key session.Key) (transcript, error) {
	if rs.history != nil {
		h, err := rs.history.Load(ctx, rs.conversation(key))
		return transcript{msgs: h.Messages, cursor: h.Cursor}, errors.Wrap(err, "chat: load history")
	}
	sess, err := rs.sessions.GetSession(ctx, key)
	return transcript{sess: sess}, err
}

// each 依次给出历史中的消息；来自会话时同时给出所在的事件，否则 ev 为 nil。
func (t transcript) each(fn func(ev *event.Event, m model.Message)) {
	if t.sess == nil {
		for _, m := range t.msgs {
			fn(nil, m)
		}
		return
	}
	for i := range t.sess.Events {
		ev := &t.sess.Events[i]
		if ev.Response == nil {
			continue
		}
		for _, c := range ev.Choices {
			fn(ev, c.Message)
		}
	}
}
