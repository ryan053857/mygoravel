package ai_test

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
	"trpc.group/trpc-go/trpc-agent-go/tool/function"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
)

type orderArgs struct {
	OrderID string `json:"order_id"`
}

type order struct {
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
}

func getOrder(delay time.Duration) tool.CallableTool {
	return function.NewFunctionTool(func(ctx context.Context, a orderArgs) (order, error) {
		time.Sleep(delay)
		return order{a.OrderID, "delivered"}, nil
	}, function.WithName("get_order"), function.WithDescription("查询订单"))
}

type refunds struct{ done []string }

func (r *refunds) tool() tool.CallableTool {
	return function.NewFunctionTool(func(ctx context.Context, a orderArgs) (string, error) {
		r.done = append(r.done, a.OrderID)
		return "refunded " + a.OrderID, nil
	}, function.WithName("refund_order"), function.WithDescription("退款"))
}

func newRuns(t *testing.T, m *aitest.Model, tools []tool.Tool, opts ...ai.Option) *ai.Runs {
	t.Helper()
	ag := llmagent.New("support",
		llmagent.WithModel(m),
		llmagent.WithTools(tools),
		llmagent.WithGenerationConfig(model.GenerationConfig{Stream: true}),
	)
	sessions := inmemory.NewSessionService()
	rs := ai.NewRuns("test", ag, sessions, opts...)
	t.Cleanup(func() {
		_ = rs.Close()
		_ = sessions.Close()
	})
	return rs
}

// collect 读完运行的事件，返回 "name:摘要" 列表。
func collect(t *testing.T, r *ai.Run, lastEventID string) ([]string, []ai.Event) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var names []string
	var events []ai.Event
	for ev := range r.Events(ctx, lastEventID) {
		events = append(events, ev)
		s := ev.Name
		switch d := ev.Data.(type) {
		case ai.Text:
			s += ":" + d.Text
		case ai.ToolCall:
			s += ":" + d.Name
		case ai.ToolResult:
			s += ":" + d.Name
		}
		names = append(names, s)
	}
	if ctx.Err() != nil {
		t.Fatalf("run did not finish, events so far: %q", names)
	}
	return names, events
}

func messagesOf(req []model.Message) string {
	var b []string
	for _, m := range req {
		s := string(m.Role)
		if m.Content != "" {
			s += ":" + m.Content
		}
		for _, tc := range m.ToolCalls {
			s += ":call " + tc.Function.Name
		}
		b = append(b, s)
	}
	return strings.Join(b, " | ")
}

func TestStartStreamsAnswer(t *testing.T) {
	m := aitest.NewModel(aitest.Reply{Reasoning: "想一想", Text: "你好 世界"})
	rs := newRuns(t, m, nil)
	r, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	names, events := collect(t, r, "")
	if got := strings.Join(names, ","); got != "reasoning:想一想,text:你好 ,text:世界,done" {
		t.Fatalf("events %s", got)
	}
	if d := events[len(events)-1].Data.(ai.Done); len(d.Pending) != 0 || d.Canceled {
		t.Fatalf("done %+v", d)
	}

	// 从中间的事件 ID 接上，只收到之后的事件。
	rest, _ := collect(t, r, events[1].ID)
	if got := strings.Join(rest, ","); got != "text:世界,done" {
		t.Fatalf("resumed events %s", got)
	}
	if got, err := rs.Get(context.Background(), "u1", "c1"); err != nil || got != r {
		t.Fatal("finished run should be kept for reconnects")
	}
}

func TestToolCallsAndBusy(t *testing.T) {
	m := aitest.NewModel(
		// 只有思考过程、没有正文的回复：流式发出的思考过程不应从完整消息中重复发送。
		aitest.Reply{Reasoning: "查一下", Calls: []aitest.Call{{Name: "get_order", Args: orderArgs{"A1"}}}},
		aitest.Text("已签收"),
	)
	rs := newRuns(t, m, []tool.Tool{getOrder(200 * time.Millisecond)})
	r, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("查 A1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("again")); !errors.Is(err, ai.ErrBusy) {
		t.Fatalf("second start: %v", err)
	}
	names, _ := collect(t, r, "")
	if got := strings.Join(names, ","); got != "reasoning:查一下,tool_call:get_order,tool_result:get_order,text:已签收,done" {
		t.Fatalf("events %s", got)
	}
	if _, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("again")); err != nil {
		t.Fatalf("start after finish: %v", err)
	}
}

func TestSteerDuringToolCall(t *testing.T) {
	m := aitest.NewModel(
		aitest.Calls(aitest.Call{Name: "get_order", Args: orderArgs{"A1"}}),
		aitest.Text("都查好了"),
	)
	rs := newRuns(t, m, []tool.Tool{getOrder(300 * time.Millisecond)})
	r, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("查 A1"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := r.Steer(context.Background(), model.NewUserMessage("还有 B2")); err != nil {
		t.Fatal(err)
	}
	names, _ := collect(t, r, "")
	if got := strings.Join(names, ","); got != "tool_call:get_order,tool_result:get_order,user:还有 B2,text:都查好了,done" {
		t.Fatalf("events %s", got)
	}
	if got := messagesOf(m.LastRequest()); !strings.HasSuffix(got, `| tool:{"order_id":"A1","status":"delivered"} | user:还有 B2`) {
		t.Fatalf("second request %s", got)
	}
}

func TestSteerDuringFinalAnswerContinues(t *testing.T) {
	m := aitest.NewModel(
		aitest.Reply{Text: "第一个回答", Delay: 300 * time.Millisecond},
		aitest.Text("第二个回答"),
	)
	rs := newRuns(t, m, nil)
	r, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("问题一"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := r.Steer(context.Background(), model.NewUserMessage("问题二")); err != nil {
		t.Fatal(err)
	}
	names, _ := collect(t, r, "")
	if got := strings.Join(names, ","); got != "text:第一个回答,user:问题二,text:第二个回答,done" {
		t.Fatalf("events %s", got)
	}
	if got := messagesOf(m.LastRequest()); !strings.HasSuffix(got, "user:问题一 | assistant:第一个回答 | user:问题二") {
		t.Fatalf("second request %s", got)
	}
	if err := r.Steer(context.Background(), model.NewUserMessage("太晚了")); !errors.Is(err, ai.ErrFinished) {
		t.Fatalf("steer after finish: %v", err)
	}
}

func TestApprovalAcrossRuns(t *testing.T) {
	m := aitest.NewModel(
		aitest.Calls(
			aitest.Call{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}},
			aitest.Call{ID: "r2", Name: "refund_order", Args: orderArgs{"B2"}},
			aitest.Call{ID: "g1", Name: "get_order", Args: orderArgs{"C3"}},
		),
		aitest.Text("A1 已退款，B2 被拒绝。"),
	)
	var refunded refunds
	refund := refunded.tool()
	rs := newRuns(t, m, []tool.Tool{refund, getOrder(0)}, ai.RequireApproval(refund))
	ctx := context.Background()
	r, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("退款 A1 和 B2，再查 C3"))
	if err != nil {
		t.Fatal(err)
	}
	names, events := collect(t, r, "")
	done := events[len(events)-1].Data.(ai.Done)
	if len(done.Pending) != 2 || len(refunded.done) != 0 {
		t.Fatalf("events %q, done %+v, refunded %v", names, done, refunded.done)
	}
	if !strings.Contains(strings.Join(names, ","), "tool_result:get_order") {
		t.Fatalf("unapproved tools should still run: %q", names)
	}

	pending, err := rs.Pending(ctx, "u1", "c1")
	if err != nil || len(pending) != 2 {
		t.Fatalf("pending %+v, %v", pending, err)
	}
	if _, err := rs.Approve(ctx, "u1", "c1", "nope"); !errors.Is(err, ai.ErrNotPending) {
		t.Fatalf("approve unknown call: %v", err)
	}
	next, err := rs.Approve(ctx, "u1", "c1", "r1")
	if err != nil || next != nil {
		t.Fatalf("first decision should wait for the other: %v %v", next, err)
	}
	if len(refunded.done) != 1 || refunded.done[0] != "A1" {
		t.Fatalf("refunded %v", refunded.done)
	}
	next, err = rs.Reject(ctx, "u1", "c1", "r2", "金额超限")
	if err != nil || next == nil {
		t.Fatalf("last decision should continue: %v %v", next, err)
	}
	names, events = collect(t, next, "")
	if got := strings.Join(names, ","); got != "tool_result:refund_order,tool_result:refund_order,text:A1 ,text:已退款，B2 ,text:被拒绝。,done" {
		t.Fatalf("events after approval %s", got)
	}
	// 继续的运行先发出这一批调用的结果，被拒绝的标记为 Denied。
	if r1, r2 := events[0].Data.(ai.ToolResult), events[1].Data.(ai.ToolResult); r1.ID != "r1" || r1.Denied || r2.ID != "r2" || !r2.Denied {
		t.Fatalf("results %+v %+v", r1, r2)
	}
	req := m.LastRequest()
	var results []string
	for _, msg := range req {
		if msg.Role == model.RoleTool {
			results = append(results, msg.ToolID+"="+msg.Content)
		}
	}
	got := strings.Join(results, " ")
	if !strings.Contains(got, "r1=refunded A1") || !strings.Contains(got, `r2={"approval":"rejected","reason":"金额超限"}`) || !strings.Contains(got, "g1=") {
		t.Fatalf("tool results sent to model: %s\nfull request: %s", got, messagesOf(req))
	}
	if pending, _ := rs.Pending(ctx, "u1", "c1"); len(pending) != 0 {
		t.Fatalf("still pending %+v", pending)
	}
}

func TestCancel(t *testing.T) {
	m := aitest.NewModel(aitest.Reply{Text: "slow", Delay: 5 * time.Second})
	rs := newRuns(t, m, nil)
	r, err := rs.Start(context.Background(), "u1", "c1", model.NewUserMessage("hi"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	r.Cancel()
	names, events := collect(t, r, "")
	if len(names) != 1 || !events[0].Data.(ai.Done).Canceled {
		t.Fatalf("events %q", names)
	}
}

func TestSendStartsOrSteers(t *testing.T) {
	m := aitest.NewModel(
		aitest.Calls(aitest.Call{Name: "get_order", Args: orderArgs{"A1"}}),
		aitest.Text("好的"),
	)
	rs := newRuns(t, m, []tool.Tool{getOrder(300 * time.Millisecond)})
	ctx := context.Background()
	r, err := rs.Send(ctx, "u1", "c1", model.NewUserMessage("查 A1"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	same, err := rs.Send(ctx, "u1", "c1", model.NewUserMessage("还有 B2"))
	if err != nil || same != r {
		t.Fatalf("second Send should steer the running run: %v", err)
	}
	names, _ := collect(t, r, "")
	if !strings.Contains(strings.Join(names, ","), "user:还有 B2") {
		t.Fatalf("events %q", names)
	}
}

func TestSteerBeforeApprovalIsDeliveredAfterIt(t *testing.T) {
	m := aitest.NewModel(
		aitest.Reply{Calls: []aitest.Call{{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}}, Delay: 300 * time.Millisecond},
		aitest.Text("已退款，B2 也查好了"),
	)
	var refunded refunds
	refund := refunded.tool()
	rs := newRuns(t, m, []tool.Tool{refund}, ai.RequireApproval(refund), ai.KeepFinished(time.Millisecond))
	ctx := context.Background()
	r, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("退款 A1"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if err := r.Steer(context.Background(), model.NewUserMessage("还有 B2")); err != nil {
		t.Fatal(err)
	}
	<-r.Done()
	time.Sleep(20 * time.Millisecond) // 超过保留期

	if _, err := rs.Start(ctx, "u1", "c1", model.NewUserMessage("新问题")); !errors.Is(err, ai.ErrAwaitingApproval) {
		t.Fatalf("start while awaiting approval: %v", err)
	}
	next, err := rs.Approve(ctx, "u1", "c1", "r1")
	if err != nil || next == nil {
		t.Fatalf("approve: %v %v", next, err)
	}
	names, _ := collect(t, next, "")
	if got := strings.Join(names, ","); !strings.HasPrefix(got, "tool_result:refund_order,user:还有 B2,text:") {
		t.Fatalf("events after approval %s", got)
	}
	if got := messagesOf(m.LastRequest()); !strings.HasSuffix(got, "tool:refunded A1 | user:还有 B2") {
		t.Fatalf("request after approval %s", got)
	}
}
