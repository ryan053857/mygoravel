package ai_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
	"github.com/ryan053857/mygoravel/httpx/rest"
)

type chatInput struct {
	Body ai.UIRequest
}

type resumeInput struct {
	ID string `path:"id"`
}

// chatServer 按 useChat 的约定注册 POST /chat 和 GET /chat/{id}/stream。
func chatServer(t *testing.T, rs *ai.Runs) string {
	t.Helper()
	api := rest.New(rest.Options{})
	post := huma.Operation{OperationID: "chat", Method: http.MethodPost, Path: "/chat"}
	rest.Public()(&post)
	rest.RegisterSSE(api, post, ai.UIEvents, func(ctx context.Context, in *chatInput) (*huma.StreamResponse, error) {
		run, err := rs.Submit(ctx, "u1", in.Body)
		if err != nil {
			return nil, err
		}
		return ai.UIStream(run), nil
	})
	get := huma.Operation{OperationID: "chat-resume", Method: http.MethodGet, Path: "/chat/{id}/stream"}
	rest.Public()(&get)
	rest.RegisterSSE(api, get, ai.UIEvents, func(ctx context.Context, in *resumeInput) (*huma.StreamResponse, error) {
		run, err := rs.Get(ctx, "u1", in.ID)
		if err != nil {
			return nil, err
		}
		return ai.UIResume(run), nil
	})
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv.URL
}

// chunks 读出 UI 消息流，返回各片段的摘要，例如 "text-delta:你好"。
func chunks(t *testing.T, resp *http.Response) []string {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("x-vercel-ai-ui-message-stream") != "v1" {
		t.Fatalf("status %d, headers %v", resp.StatusCode, resp.Header)
	}
	var out []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok {
			continue
		}
		if data == "[DONE]" {
			return append(out, "[DONE]")
		}
		var c map[string]any
		if err := json.Unmarshal([]byte(data), &c); err != nil {
			t.Fatalf("bad chunk %q: %v", data, err)
		}
		s := c["type"].(string)
		for _, k := range []string{"delta", "toolName", "toolCallId", "finishReason"} {
			if v, ok := c[k]; ok && (k != "toolCallId" || !strings.HasPrefix(s, "tool-input")) {
				s += ":" + v.(string)
			}
		}
		if parts, ok := c["parts"].([]any); ok {
			s += ":" + parts[0].(map[string]any)["text"].(string)
		}
		out = append(out, s)
	}
	t.Fatalf("stream ended without [DONE]: %q", out)
	return nil
}

func postChat(t *testing.T, url string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := http.Post(url+"/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func userMessage(text string) map[string]any {
	return map[string]any{"id": "m1", "role": "user", "parts": []any{map[string]any{"type": "text", "text": text}}}
}

func TestUIStreamWithApproval(t *testing.T) {
	m := aitest.NewModel(
		aitest.Reply{Reasoning: "先查", Text: "好的", Calls: []aitest.Call{
			{ID: "g1", Name: "get_order", Args: orderArgs{"C3"}},
			{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}},
		}},
		aitest.Text("已退款"),
	)
	var refunded refunds
	refund := refunded.tool()
	rs := newRuns(t, m, []tool.Tool{refund, getOrder(0)}, ai.RequireApproval(refund))
	url := chatServer(t, rs)

	// useChat 只发送最后一条消息（prepareSendMessagesRequest）。
	got := chunks(t, postChat(t, url, map[string]any{"id": "c1", "message": userMessage("退款 A1")}))
	want := []string{
		"start", "start-step",
		"reasoning-start", "reasoning-delta:先查", "reasoning-end",
		"text-start", "text-delta:好的", "text-end",
		"tool-input-available:get_order", "tool-input-available:refund_order",
		"tool-output-available:g1",
		"tool-approval-request:r1",
		"finish-step", "finish:tool-calls", "[DONE]",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	// 页面重新打开时的历史：退款等待审批。
	history, err := rs.UIMessages(context.Background(), "u1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(history)
	for _, s := range []string{`"role":"user"`, `"type":"tool-refund_order","state":"approval-requested","toolCallId":"r1"`, `"approval":{"id":"r1"}`, `"type":"tool-get_order","state":"output-available","toolCallId":"g1"`, `"type":"reasoning","text":"先查"`} {
		if !strings.Contains(string(b), s) {
			t.Fatalf("history lacks %s: %s", s, b)
		}
	}

	// useChat 按默认设置发送全部消息，最后一条是带审批结果的助手消息。
	var approved map[string]any
	_ = json.Unmarshal(b, &[]any{nil, &approved})
	for _, p := range approved["parts"].([]any) {
		part := p.(map[string]any)
		if part["toolCallId"] == "r1" {
			part["state"] = "approval-responded"
			part["approval"] = map[string]any{"id": "r1", "approved": true}
		}
	}
	got = chunks(t, postChat(t, url, map[string]any{"id": "c1", "messages": []any{userMessage("退款 A1"), approved}, "trigger": "submit-message"}))
	want = []string{"start", "tool-output-available:r1", "start-step", "text-start", "text-delta:已退款", "text-end", "finish-step", "finish:stop", "[DONE]"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("after approval got %q", got)
	}
	if len(refunded.done) != 1 {
		t.Fatalf("refunded %v", refunded.done)
	}
}

func TestUIStreamRejectAndSteer(t *testing.T) {
	m := aitest.NewModel(
		aitest.Calls(aitest.Call{ID: "g1", Name: "get_order", Args: orderArgs{"A1"}}),
		aitest.Calls(aitest.Call{ID: "r1", Name: "refund_order", Args: orderArgs{"A1"}}),
		aitest.Text("好的，不退了"),
	)
	var refunded refunds
	refund := refunded.tool()
	rs := newRuns(t, m, []tool.Tool{refund, getOrder(300 * time.Millisecond)}, ai.RequireApproval(refund))
	url := chatServer(t, rs)

	resp := postChat(t, url, map[string]any{"id": "c1", "message": userMessage("查 A1")})
	time.Sleep(100 * time.Millisecond)
	if err := rs.Steer(context.Background(), "u1", "c1", model.NewUserMessage("顺便退款")); err != nil {
		t.Fatal(err)
	}
	got := chunks(t, resp)
	want := []string{
		"start", "start-step", "tool-input-available:get_order", "tool-output-available:g1",
		"finish-step", "steering-message:顺便退款",
		"start-step", "tool-input-available:refund_order", "tool-approval-request:r1",
		"finish-step", "finish:tool-calls", "[DONE]",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got  %q\nwant %q", got, want)
	}

	reject := map[string]any{"id": "a1", "role": "assistant", "parts": []any{map[string]any{
		"type": "tool-refund_order", "toolCallId": "r1", "state": "approval-responded", "input": map[string]any{"order_id": "A1"},
		"approval": map[string]any{"id": "r1", "approved": false, "reason": "不需要"},
	}}}
	got = chunks(t, postChat(t, url, map[string]any{"id": "c1", "message": reject}))
	if strings.Join(got[:2], "|") != "start|tool-output-denied:r1" || len(refunded.done) != 0 {
		t.Fatalf("after reject got %q, refunded %v", got, refunded.done)
	}

	history, _ := rs.UIMessages(context.Background(), "u1", "c1")
	b, _ := json.Marshal(history)
	for _, s := range []string{`"type":"steering-message"`, `"state":"output-denied"`, `"approval":{"id":"r1","approved":false,"reason":"不需要"}`} {
		if !strings.Contains(string(b), s) {
			t.Fatalf("history lacks %s: %s", s, b)
		}
	}
	if n := strings.Count(string(b), `"role":"assistant"`); n != 1 {
		t.Fatalf("steering should stay inside one assistant message, got %d: %s", n, b)
	}
}

func TestUIResume(t *testing.T) {
	m := aitest.NewModel(aitest.Reply{Text: "慢 回答", Delay: 300 * time.Millisecond})
	rs := newRuns(t, m, nil)
	url := chatServer(t, rs)

	resp, err := http.Get(url + "/chat/c1/stream")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("no run: status %d", resp.StatusCode)
	}

	first := postChat(t, url, map[string]any{"id": "c1", "message": userMessage("hi")})
	first.Body.Close() // 断线
	resp, err = http.Get(url + "/chat/c1/stream")
	if err != nil {
		t.Fatal(err)
	}
	// 续传从头重放整次运行。
	if got := strings.Join(chunks(t, resp), "|"); got != "start|start-step|text-start|text-delta:慢 |text-delta:回答|text-end|finish-step|finish:stop|[DONE]" {
		t.Fatalf("resumed %s", got)
	}
	resp, _ = http.Get(url + "/chat/c1/stream")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("finished run: status %d", resp.StatusCode)
	}
}

func TestSubmitRejectsBadRequests(t *testing.T) {
	rs := newRuns(t, aitest.NewModel(), nil)
	url := chatServer(t, rs)
	for _, body := range []any{
		map[string]any{"id": "c1"},
		map[string]any{"id": "c1", "message": userMessage("")},
		map[string]any{"id": "c1", "message": map[string]any{"id": "a", "role": "assistant", "parts": []any{}}},
	} {
		resp := postChat(t, url, body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("body %v: status %d", body, resp.StatusCode)
		}
	}
}
