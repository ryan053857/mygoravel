package chat

import (
	"context"
	"encoding/json"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

// appendExtension 标记由 Runs.Append 加入、不是模型生成的消息，值是作者。
const appendExtension = "mygoravel.ai.appended"

// Append 在对话中加入一条不是模型生成的消息，例如人写的回复、系统通知、从其他渠道同步来的消息。
// 对话不存在时自动创建。
//
// author 标明消息来自谁，例如 "staff:42"、"system"。模型在之后的回答中看到它：trpc-agent-go 把
// 作者不是当前 agent 的消息作为用户消息交给模型，agent 用 llmagent.WithAddContextPrefix(true) 创建时
// 带上 "[author] said:" 前缀，模型才知道是谁说的。
// UIMessages 把它作为单独的一条消息返回，metadata.author 是 author，不会并入前后的回答。
// msg 的角色是 user 或 assistant，内容不能为空。trpc-agent-go 丢弃第一条用户消息之前的内容，
// 所以对话中还没有用户消息时，msg 必须是 user 角色。
//
// 对话正在回答时返回 ErrBusy（回答过程中加入用户的消息用 Steer）；有等待审批的工具调用时返回
// ErrAwaitingApproval：工具调用和它的结果之间不能插入其他消息。
func (rs *Runs) Append(ctx context.Context, userID, sessionID, author string, msg model.Message) error {
	switch {
	case author == "":
		return errors.New("chat: append needs an author")
	case msg.Role != model.RoleUser && msg.Role != model.RoleAssistant:
		return errors.Newf("chat: cannot append a %q message", msg.Role)
	case msg.Content == "" && len(msg.ContentParts) == 0:
		return errors.New("chat: cannot append an empty message")
	}
	if rs.history != nil {
		return ErrNeedsSessions
	}
	key := rs.key(userID, sessionID)

	// 加入期间不让这个对话开始运行：本实例用 appending 标记，其他实例用对话锁。
	rs.mu.Lock()
	if r, ok := rs.runs[key]; rs.appending[key] || (ok && r.remote == nil && !r.isFinished()) {
		rs.mu.Unlock()
		return ErrBusy.New()
	}
	rs.appending[key] = true
	rs.mu.Unlock()
	defer func() {
		rs.mu.Lock()
		delete(rs.appending, key)
		rs.mu.Unlock()
	}()
	if rs.cluster != nil {
		own, err := rs.cluster.acquire(ctx, key)
		if err != nil {
			return err
		}
		defer own.release()
	}

	t, pending, err := rs.pending(ctx, key)
	if err != nil {
		return err
	}
	sess := t.sess
	if len(pending) > 0 {
		return ErrAwaitingApproval.New()
	}
	if !hasUserMessage(sess) && msg.Role != model.RoleUser {
		return errors.New("chat: a conversation must start with a user message")
	}
	if sess == nil {
		if sess, err = rs.sessions.CreateSession(ctx, key, session.StateMap{}); err != nil {
			return err
		}
	}
	mark, err := json.Marshal(author)
	if err != nil {
		return err
	}
	evt := event.NewResponseEvent(uuid.NewString(), author, &model.Response{
		Object:  model.ObjectTypeChatCompletion,
		Done:    true,
		Choices: []model.Choice{{Message: msg}},
	})
	evt.Extensions = map[string]json.RawMessage{appendExtension: mark}
	return errors.Wrap(rs.sessions.AppendEvent(ctx, sess, evt), "chat: append message")
}

// hasUserMessage 报告对话中是否已经有用户消息。
func hasUserMessage(sess *session.Session) bool {
	if sess == nil {
		return false
	}
	for i := range sess.Events {
		if sess.Events[i].Response != nil && sess.Events[i].IsUserMessage() {
			return true
		}
	}
	return false
}

// appendedBy 返回 Append 加入的消息的作者。
func appendedBy(ev *event.Event) (string, bool) {
	raw, ok := ev.Extensions[appendExtension]
	if !ok {
		return "", false
	}
	var author string
	return author, json.Unmarshal(raw, &author) == nil
}
