// Package chattest 提供按脚本回复的模型，用于测试 agent，不访问真实的模型服务。
package chattest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

// Reply 是模型的一次回复。
type Reply struct {
	// Text 是回答，按空格拆成多个增量流式发出。
	Text string
	// Reasoning 是思考过程，在回答之前发出。
	Reasoning string
	// Calls 是这次回复调用的工具。
	Calls []Call
	// Delay 是开始回复前等待的时间，用于模拟慢模型。
	Delay time.Duration
}

// Call 是一次工具调用。ID 为空时自动生成。
type Call struct {
	ID   string
	Name string
	Args any
}

// Model 按顺序给出 Reply，并记录收到的请求。脚本用完后返回错误。
type Model struct {
	mu       sync.Mutex
	replies  []Reply
	requests []*model.Request
}

// NewModel 创建按 replies 依次回复的模型。
func NewModel(replies ...Reply) *Model { return &Model{replies: replies} }

// Text 返回只回答 text 的 Reply。
func Text(text string) Reply { return Reply{Text: text} }

// Calls 返回调用工具的 Reply。
func Calls(calls ...Call) Reply { return Reply{Calls: calls} }

// Requests 返回目前收到的请求。
func (m *Model) Requests() []*model.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*model.Request(nil), m.requests...)
}

// LastRequest 返回最后一次请求的消息。
func (m *Model) LastRequest() []model.Message {
	reqs := m.Requests()
	if len(reqs) == 0 {
		return nil
	}
	return reqs[len(reqs)-1].Messages
}

// Info 实现 model.Model。
func (m *Model) Info() model.Info { return model.Info{Name: "chattest"} }

// GenerateContent 实现 model.Model。
func (m *Model) GenerateContent(ctx context.Context, req *model.Request) (<-chan *model.Response, error) {
	m.mu.Lock()
	m.requests = append(m.requests, req)
	n := len(m.requests)
	if n > len(m.replies) {
		m.mu.Unlock()
		return nil, fmt.Errorf("chattest: unexpected request %d, script has %d replies", n, len(m.replies))
	}
	reply := m.replies[n-1]
	m.mu.Unlock()

	msg := model.Message{Role: model.RoleAssistant, Content: reply.Text, ReasoningContent: reply.Reasoning}
	for i, c := range reply.Calls {
		args, err := json.Marshal(c.Args)
		if err != nil {
			return nil, err
		}
		id := c.ID
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", n, i)
		}
		msg.ToolCalls = append(msg.ToolCalls, model.ToolCall{
			Type:     "function",
			ID:       id,
			Function: model.FunctionDefinitionParam{Name: c.Name, Arguments: args},
		})
	}

	ch := make(chan *model.Response)
	go func() {
		defer close(ch)
		send := func(r *model.Response) bool {
			r.Model, r.Timestamp, r.Created = "chattest", time.Now(), time.Now().Unix()
			select {
			case ch <- r:
				return true
			case <-ctx.Done():
				return false
			}
		}
		select {
		case <-time.After(reply.Delay):
		case <-ctx.Done():
			return
		}
		stream := req.GenerationConfig.Stream
		if stream && reply.Reasoning != "" {
			if !send(partial(model.Message{Role: model.RoleAssistant, ReasoningContent: reply.Reasoning})) {
				return
			}
		}
		if stream && reply.Text != "" {
			for _, piece := range strings.SplitAfter(reply.Text, " ") {
				if !send(partial(model.Message{Role: model.RoleAssistant, Content: piece})) {
					return
				}
			}
		}
		send(&model.Response{
			Object:  model.ObjectTypeChatCompletion,
			Done:    true,
			Choices: []model.Choice{{Message: msg}},
		})
	}()
	return ch, nil
}

func partial(delta model.Message) *model.Response {
	return &model.Response{
		Object:    model.ObjectTypeChatCompletionChunk,
		IsPartial: true,
		Choices:   []model.Choice{{Delta: delta}},
	}
}
