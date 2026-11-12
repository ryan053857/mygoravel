package chat_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/ai/chat"
	"github.com/ryan053857/mygoravel/ai/chat/chattest"
)

func TestAppend(t *testing.T) {
	t.Parallel()
	m := chattest.NewModel(chattest.Text("你好，我是助手"), chattest.Text("不客气"))
	ag := llmagent.New("support", llmagent.WithModel(m), llmagent.WithAddContextPrefix(true),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}))
	sessions := inmemory.NewSessionService()
	rs := chat.NewRuns("test", ag, sessions)
	t.Cleanup(func() {
		_ = rs.Close()
		_ = sessions.Close()
	})
	ctx := context.Background()
	first, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("在吗"))
	if err != nil {
		t.Fatal(err)
	}
	<-first.Done()

	if err := rs.Append(ctx, "u1", "c1", "staff:42", model.NewAssistantMessage("我是人工客服小王，已为你查询")); err != nil {
		t.Fatal(err)
	}
	if err := rs.Append(ctx, "u1", "c1", "system", model.NewUserMessage("订单 A1 已发货")); err != nil {
		t.Fatal(err)
	}
	second, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("谢谢"))
	if err != nil {
		t.Fatal(err)
	}
	<-second.Done()

	// 模型看到加入的消息，并知道是谁说的。
	var prompt []string
	for _, msg := range m.LastRequest() {
		prompt = append(prompt, msg.Content)
		for _, p := range msg.ContentParts {
			if p.Text != nil {
				prompt = append(prompt, *p.Text)
			}
		}
	}
	joined := strings.Join(prompt, "\n")
	for _, want := range []string{"[staff:42] said:", "我是人工客服小王", "[system] said:", "订单 A1 已发货", "谢谢"} {
		if !strings.Contains(joined, want) {
			t.Errorf("prompt lacks %q:\n%s", want, joined)
		}
	}

	// 历史中，加入的消息单独成为一条并带有作者，不并入前后的回答。
	msgs, err := rs.UIMessages(ctx, "u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, msg := range msgs {
		desc := msg.Role
		if msg.Metadata != nil {
			desc += "(" + msg.Metadata.Author + ")"
		}
		got = append(got, desc)
	}
	if want := "user assistant assistant(staff:42) user(system) user assistant"; strings.Join(got, " ") != want {
		t.Fatalf("history %v, want %s", got, want)
	}
	if msgs[2].Parts[0].Text != "我是人工客服小王，已为你查询" {
		t.Fatalf("appended text %+v", msgs[2].Parts)
	}
}

func TestAppendCreatesConversation(t *testing.T) {
	t.Parallel()
	rs := newRuns(t, chattest.NewModel(), nil)
	ctx := context.Background()
	// trpc-agent-go 丢弃第一条用户消息之前的内容：拒绝，而不是悄悄丢掉。
	if err := rs.Append(ctx, "u1", "new", "system", model.NewAssistantMessage("欢迎")); err == nil {
		t.Fatal("assistant message accepted as the first message")
	}
	if err := rs.Append(ctx, "u1", "new", "web", model.NewUserMessage("用户在网页上留言：订单没收到")); err != nil {
		t.Fatal(err)
	}
	msgs, err := rs.UIMessages(ctx, "u1", "new")
	if err != nil || len(msgs) != 1 || msgs[0].Role != "user" || msgs[0].Metadata.Author != "web" {
		t.Fatalf("history %+v err %v", msgs, err)
	}
}

func TestAppendRejects(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	t.Run("invalid", func(t *testing.T) {
		rs := newRuns(t, chattest.NewModel(), nil)
		for _, tc := range []struct {
			author string
			msg    model.Message
		}{
			{"", model.NewUserMessage("x")},
			{"system", model.NewSystemMessage("x")},
			{"system", model.NewUserMessage("")},
		} {
			if err := rs.Append(ctx, "u1", "c1", tc.author, tc.msg); err == nil {
				t.Errorf("append %q %+v accepted", tc.author, tc.msg)
			}
		}
	})
	t.Run("while running", func(t *testing.T) {
		rs := newRuns(t, chattest.NewModel(chattest.Reply{Text: "慢", Delay: 300 * time.Millisecond}), nil)
		r, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("hi"))
		if err != nil {
			t.Fatal(err)
		}
		if err := rs.Append(ctx, "u1", "c1", "system", model.NewUserMessage("x")); !errors.Is(err, chat.ErrBusy) {
			t.Fatalf("append while running: %v", err)
		}
		<-r.Done()
	})
	t.Run("awaiting approval", func(t *testing.T) {
		m := chattest.NewModel(chattest.Reply{Calls: []chattest.Call{{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}}})
		var refunded refunds
		refund := refunded.tool()
		rs := newRuns(t, m, []tool.Tool{refund}, chat.RequireApproval(refund))
		r, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("退款 A1"))
		if err != nil {
			t.Fatal(err)
		}
		<-r.Done()
		if err := rs.Append(ctx, "u1", "c1", "system", model.NewUserMessage("x")); !errors.Is(err, chat.ErrAwaitingApproval) {
			t.Fatalf("append while awaiting approval: %v", err)
		}
	})
}
