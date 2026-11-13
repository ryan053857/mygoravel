package chat

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/logging"
)

// 本文件实现 Vercel AI SDK 的 UI 消息流协议（https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol），
// 前端可以直接使用 @ai-sdk/react、@ai-sdk/vue 等的 useChat。

// ErrBadRequest 表示请求中没有可以处理的消息。
var ErrBadRequest = errorx.Define(errorx.InvalidArgument, "ai.bad_request")

// UIEvents 描述 UI 消息流，传给 rest.RegisterSSE 生成 OpenAPI 文档。
var UIEvents = map[string]any{"message": UIChunk{}}

// UIChunk 是 UI 消息流中的一个片段，Type 决定其余哪些字段有值。
type UIChunk struct {
	Type         string   `json:"type"`
	ID           string   `json:"id,omitempty"`
	Delta        string   `json:"delta,omitempty"`
	ToolCallID   string   `json:"toolCallId,omitempty"`
	ToolName     string   `json:"toolName,omitempty"`
	Input        any      `json:"input,omitempty"`
	Output       any      `json:"output,omitempty"`
	ApprovalID   string   `json:"approvalId,omitempty"`
	ErrorText    string   `json:"errorText,omitempty"`
	FinishReason string   `json:"finishReason,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Parts        []UIPart `json:"parts,omitempty"`
}

// UIMessage 是 useChat 中的一条消息。
type UIMessage struct {
	_     struct{} `additionalProperties:"true"`
	ID    string   `json:"id"`
	Role  string   `json:"role" enum:"system,user,assistant"`
	Parts []UIPart `json:"parts"`
	// Metadata 是消息的附加信息。Runs.Append 加入的消息带有 author，模型的回答和用户发送的消息没有。
	Metadata *UIMetadata `json:"metadata,omitempty"`
}

// UIMetadata 是 UIMessage 的附加信息。
type UIMetadata struct {
	_ struct{} `additionalProperties:"true"`
	// Author 是 Runs.Append 加入消息时给出的作者。
	Author string `json:"author,omitempty"`
}

// UIPart 是消息的一部分：文本、思考过程、工具调用、插话等。
type UIPart struct {
	_          struct{}    `additionalProperties:"true"`
	Type       string      `json:"type"`
	ID         string      `json:"id,omitempty"`
	Text       string      `json:"text,omitempty"`
	State      string      `json:"state,omitempty"`
	ToolCallID string      `json:"toolCallId,omitempty"`
	ToolName   string      `json:"toolName,omitempty"`
	Input      any         `json:"input,omitempty"`
	Output     any         `json:"output,omitempty"`
	ErrorText  string      `json:"errorText,omitempty"`
	Approval   *UIApproval `json:"approval,omitempty"`
	Parts      []UIPart    `json:"parts,omitempty"`
}

// UIApproval 是工具调用的审批状态。
type UIApproval struct {
	_        struct{} `additionalProperties:"true"`
	ID       string   `json:"id"`
	Approved *bool    `json:"approved,omitempty"`
	Reason   string   `json:"reason,omitempty"`
}

// UIRequest 是 useChat 发送消息时的请求体。
//
// 服务端保存着对话历史，前端只需要发送最后一条消息（Message）；
// 按 useChat 的默认设置发送全部消息（Messages）时，只使用最后一条。
type UIRequest struct {
	_        struct{}    `additionalProperties:"true"`
	ID       string      `json:"id" doc:"对话 ID"`
	Message  *UIMessage  `json:"message,omitempty"`
	Messages []UIMessage `json:"messages,omitempty"`
}

func (r UIRequest) last() (UIMessage, bool) {
	if r.Message != nil {
		return *r.Message, true
	}
	if len(r.Messages) > 0 {
		return r.Messages[len(r.Messages)-1], true
	}
	return UIMessage{}, false
}

// Submit 处理 useChat 发来的消息：用户消息开始一次运行；带有审批结果的助手消息
// 依次调用 Approve 或 Reject，全部决定后对话继续运行。返回的运行用 UIStream 推送给前端；
// 还有调用等待决定时返回 nil。
//
// 对话正在运行时返回 ErrBusy：useChat 会在回答结束后才发送新消息，中途插话用 Runs.Steer。
func (rs *Runs) Submit(ctx context.Context, userID string, req UIRequest, opts ...agent.RunOption) (*Run, error) {
	m, ok := req.last()
	if !ok || req.ID == "" {
		return nil, ErrBadRequest.New()
	}
	switch m.Role {
	case "user":
		text := m.text()
		if text == "" {
			return nil, ErrBadRequest.New()
		}
		return rs.Start(ctx, userID, req.ID, model.NewUserMessage(text), opts...)
	case "assistant":
		var run *Run
		decided := false
		for _, p := range m.Parts {
			if p.State != "approval-responded" || p.Approval == nil || p.Approval.Approved == nil {
				continue
			}
			decided = true
			var r *Run
			var err error
			if *p.Approval.Approved {
				r, err = rs.Approve(ctx, userID, req.ID, p.ToolCallID, opts...)
			} else {
				r, err = rs.Reject(ctx, userID, req.ID, p.ToolCallID, p.Approval.Reason, opts...)
			}
			if err != nil {
				return nil, err
			}
			if r != nil {
				run = r
			}
		}
		if !decided {
			return nil, ErrBadRequest.New()
		}
		return run, nil
	}
	return nil, ErrBadRequest.New()
}

func (m UIMessage) text() string {
	var b strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			b.WriteString(p.Text)
		}
	}
	return b.String()
}

// UIStream 以 UI 消息流推送运行的事件，直到运行结束。run 为 nil 时只发送一个空的回复。
// 它不受 httpx.Timeout 的超时限制，见 httpx.SSE。
func UIStream(run *Run) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		r, w := humachi.Unwrap(hctx)
		logStreamError(r, WriteUIStream(w, r, run))
	}}
}

// UIResume 回应 useChat 断线后的续传请求（GET {api}/{id}/stream）：运行还在进行时从头重放它的事件，
// 客户端会用重放的内容重建这条回复；没有进行中的运行时返回 204。
func UIResume(run *Run) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		r, w := humachi.Unwrap(hctx)
		if run == nil || run.isFinished() {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		logStreamError(r, WriteUIStream(w, r, run))
	}}
}

// WriteUIStream 是 UIStream 的 net/http 版本，供不使用 rest 的处理器调用。
func WriteUIStream(w http.ResponseWriter, r *http.Request, run *Run) error {
	w.Header().Set("x-vercel-ai-ui-message-stream", "v1")
	return httpx.SSE(w, r, func(s *httpx.Stream) error {
		send := func(c UIChunk) error { return s.SendJSON(httpx.Event{}, c) }
		enc := &uiEncoder{send: send}
		if err := send(UIChunk{Type: "start"}); err != nil {
			return err
		}
		if run == nil {
			if err := send(UIChunk{Type: "finish", FinishReason: "stop"}); err != nil {
				return err
			}
		} else {
			for ev := range run.Events(s.Context(), "") {
				if err := enc.event(ev); err != nil {
					return err
				}
			}
		}
		return s.Send(httpx.Event{}, "[DONE]")
	})
}

func logStreamError(r *http.Request, err error) {
	if err != nil && !errors.Is(err, context.Canceled) {
		logging.Or(nil).ErrorContext(r.Context(), "ai stream failed", logging.AttrComponent, "ai", "error", err)
	}
}

// uiEncoder 把 Event 转换为 UI 消息流片段：维护文本块和模型步骤的边界。
type uiEncoder struct {
	send       func(UIChunk) error
	block      string // 正在输出的块：text、reasoning 或空
	blockID    string
	step       bool // 是否在一个模型步骤中
	afterTools bool // 上一个事件是工具结果，模型的下一次输出属于新的步骤
	failed     bool
}

func (e *uiEncoder) event(ev Event) error {
	switch data := ev.Data.(type) {
	case Text:
		if ev.Name == EventUser {
			return e.all(e.closeBlock, e.finishStep, func() error {
				e.afterTools = false
				return e.send(UIChunk{Type: "steering-message", ID: ev.ID, Parts: []UIPart{{Type: "text", Text: data.Text}}})
			})
		}
		if err := e.all(e.nextStep, e.startStep); err != nil {
			return err
		}
		if e.block != ev.Name {
			if err := e.closeBlock(); err != nil {
				return err
			}
			e.block, e.blockID = ev.Name, ev.ID
			if err := e.send(UIChunk{Type: ev.Name + "-start", ID: ev.ID}); err != nil {
				return err
			}
		}
		return e.send(UIChunk{Type: ev.Name + "-delta", ID: e.blockID, Delta: data.Text})
	case ToolCall:
		return e.all(e.nextStep, e.startStep, e.closeBlock, func() error {
			return e.send(UIChunk{Type: "tool-input-available", ToolCallID: data.ID, ToolName: data.Name, Input: jsonValue(data.Arguments, true)})
		})
	case ToolResult:
		e.afterTools = true
		if err := e.closeBlock(); err != nil {
			return err
		}
		if data.Denied {
			return e.send(UIChunk{Type: "tool-output-denied", ToolCallID: data.ID})
		}
		return e.send(UIChunk{Type: "tool-output-available", ToolCallID: data.ID, Output: jsonValue(data.Content, false)})
	case Error:
		e.failed = true
		return e.all(e.closeBlock, func() error { return e.send(UIChunk{Type: "error", ErrorText: data.Message}) })
	case Done:
		if err := e.closeBlock(); err != nil {
			return err
		}
		for _, c := range data.Pending {
			if err := e.send(UIChunk{Type: "tool-approval-request", ApprovalID: c.ID, ToolCallID: c.ID}); err != nil {
				return err
			}
		}
		if err := e.finishStep(); err != nil {
			return err
		}
		switch {
		case data.Canceled:
			return e.send(UIChunk{Type: "abort", Reason: "canceled"})
		case e.failed:
			return e.send(UIChunk{Type: "finish", FinishReason: "error"})
		case len(data.Pending) > 0:
			return e.send(UIChunk{Type: "finish", FinishReason: "tool-calls"})
		default:
			return e.send(UIChunk{Type: "finish", FinishReason: "stop"})
		}
	}
	return nil
}

func (e *uiEncoder) all(steps ...func() error) error {
	for _, step := range steps {
		if err := step(); err != nil {
			return err
		}
	}
	return nil
}

func (e *uiEncoder) closeBlock() error {
	if e.block == "" {
		return nil
	}
	kind := e.block
	e.block = ""
	return e.send(UIChunk{Type: kind + "-end", ID: e.blockID})
}

func (e *uiEncoder) startStep() error {
	if e.step {
		return nil
	}
	e.step = true
	return e.send(UIChunk{Type: "start-step"})
}

func (e *uiEncoder) finishStep() error {
	if !e.step {
		return nil
	}
	if err := e.closeBlock(); err != nil {
		return err
	}
	e.step = false
	return e.send(UIChunk{Type: "finish-step"})
}

// nextStep 在工具结果之后结束当前步骤，模型的下一次输出开始新的步骤。
func (e *uiEncoder) nextStep() error {
	if !e.afterTools {
		return nil
	}
	e.afterTools = false
	return e.finishStep()
}

// jsonValue 把 JSON 文本原样作为值；不是合法 JSON 时作为字符串。object 为 true 时空文本视为 {}。
func jsonValue(s string, object bool) any {
	if s == "" && object {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return s
}
