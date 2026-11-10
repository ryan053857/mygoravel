package ai

import (
	"context"
	"encoding/json"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

// UIMessages 把对话历史转换为 useChat 的消息，作为页面打开时 useChat 的初始消息。
// 等待审批的工具调用处于 approval-requested 状态，前端照常显示确认按钮。对话不存在时返回空列表。
func (rs *Runs) UIMessages(ctx context.Context, userID, sessionID string) ([]UIMessage, error) {
	sess, err := rs.sessions.GetSession(ctx, rs.key(userID, sessionID))
	if err != nil || sess == nil {
		return []UIMessage{}, err
	}
	out := []UIMessage{}
	assistant := -1 // 正在拼接的助手消息在 out 中的位置
	type ref struct{ msg, part int }
	tools := map[string]ref{}
	for _, ev := range sess.Events {
		if ev.Response == nil || ev.IsPartial || ev.Error != nil {
			continue
		}
		for _, c := range ev.Choices {
			m := c.Message
			switch m.Role {
			case model.RoleUser:
				text := UIPart{Type: "text", Text: m.Content}
				if _, steer := ev.Extensions[steerExtension]; steer && assistant >= 0 {
					out[assistant].Parts = append(out[assistant].Parts, UIPart{Type: "steering-message", ID: ev.ID, Parts: []UIPart{text}})
					continue
				}
				out = append(out, UIMessage{ID: ev.ID, Role: "user", Parts: []UIPart{text}})
				assistant = -1
			case model.RoleAssistant:
				if m.Content == "" && m.ReasoningContent == "" && len(m.ToolCalls) == 0 {
					continue
				}
				if assistant < 0 {
					out = append(out, UIMessage{ID: ev.ID, Role: "assistant", Parts: []UIPart{}})
					assistant = len(out) - 1
				}
				msg := &out[assistant]
				msg.Parts = append(msg.Parts, UIPart{Type: "step-start"})
				if m.ReasoningContent != "" {
					msg.Parts = append(msg.Parts, UIPart{Type: "reasoning", Text: m.ReasoningContent, State: "done"})
				}
				if m.Content != "" {
					msg.Parts = append(msg.Parts, UIPart{Type: "text", Text: m.Content, State: "done"})
				}
				for _, tc := range m.ToolCalls {
					call := toolCall(tc)
					tools[call.ID] = ref{assistant, len(msg.Parts)}
					msg.Parts = append(msg.Parts, UIPart{
						Type:       "tool-" + call.Name,
						ToolCallID: call.ID,
						State:      "input-available",
						Input:      jsonValue(call.Arguments, true),
					})
				}
			case model.RoleTool:
				at, ok := tools[m.ToolID]
				if !ok {
					continue
				}
				part := &out[at.msg].Parts[at.part]
				name := part.Type[len("tool-"):]
				result := newToolResult(m.ToolID, name, m.Content)
				_, needsApproval := rs.approval[name]
				switch {
				case result.Denied:
					var r struct{ Reason string }
					_ = json.Unmarshal([]byte(m.Content), &r)
					part.State = "output-denied"
					part.Approval = &UIApproval{ID: m.ToolID, Approved: new(false), Reason: r.Reason}
				default:
					part.State = "output-available"
					part.Output = jsonValue(m.Content, false)
					if needsApproval {
						part.Approval = &UIApproval{ID: m.ToolID, Approved: new(true)}
					}
				}
				delete(tools, m.ToolID)
			}
		}
	}
	// 没有结果的调用：需要审批的等待审批，其余是被取消的运行留下的。
	for id, at := range tools {
		part := &out[at.msg].Parts[at.part]
		if _, ok := rs.approval[part.Type[len("tool-"):]]; ok {
			part.State = "approval-requested"
			part.Approval = &UIApproval{ID: id}
		} else {
			part.State = "output-error"
			part.ErrorText = "interrupted"
		}
	}
	return out, nil
}
