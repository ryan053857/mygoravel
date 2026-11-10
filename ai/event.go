package ai

import (
	"trpc.group/trpc-go/trpc-agent-go/event"
)

// 事件名。
const (
	EventText       = "text"        // 回答的增量文本，Data 为 Text
	EventReasoning  = "reasoning"   // 思考过程的增量文本，Data 为 Text
	EventUser       = "user"        // 一条用户消息进入了对话（包括插话），Data 为 Text
	EventToolCall   = "tool_call"   // 模型调用工具，Data 为 ToolCall
	EventToolResult = "tool_result" // 工具返回结果，Data 为 ToolResult
	EventError      = "error"       // 运行出错，Data 为 Error
	EventDone       = "done"        // 运行结束，总是最后一个事件，Data 为 Done
)

// Event 是一次运行产生的一个事件。
type Event struct {
	// ID 在一次运行中唯一且递增，可以作为 SSE 事件 ID，让页面重连时用它接上，见 Run.Events。
	ID string
	// Name 是事件名，见 EventText 等常量。
	Name string
	// Data 是事件数据，类型由 Name 决定。
	Data any
	// Raw 是产生它的 trpc-agent-go 事件；Run 自己产生的事件（例如 done）为 nil。
	Raw *event.Event
}

// Text 是一段文本。
type Text struct {
	Text string `json:"text"`
}

// ToolCall 是模型发起的一次工具调用。
type ToolCall struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments" doc:"JSON 编码的参数"`
}

// ToolResult 是一次工具调用的结果。
type ToolResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
	// Denied 表示调用被 Runs.Reject 拒绝，Content 中是拒绝的理由。
	Denied bool `json:"denied,omitempty"`
}

// Error 是运行中的错误。
type Error struct {
	Message string `json:"message"`
}

// Done 是运行的结果。
type Done struct {
	// Pending 是等待审批的工具调用，用 Runs.Approve 或 Runs.Reject 处理后对话继续。
	Pending []ToolCall `json:"pending"`
	// Canceled 表示运行被 Run.Cancel 或停机取消。
	Canceled bool `json:"canceled,omitempty"`
}
