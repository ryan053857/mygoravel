// Package ai 在 trpc-agent-go 之上运行对话式 agent：后台运行、插话、工具审批，以及把运行事件推送给客户端。
//
// agent、模型、工具和会话存储都直接使用 trpc-agent-go 的类型；本包只补上 Web 应用需要的部分：
//
//   - Runs 让一次运行脱离 HTTP 请求，在后台跑完，客户端断线后用 Last-Event-ID 接上；
//   - Run.Steer 在运行中途插入用户消息，在当前这批工具调用结束后生效，不会丢失；
//   - RequireApproval 让指定的工具停下等待审批，Runs.Approve 和 Runs.Reject 在任意进程中继续；
//   - UIStream、Submit、UIMessages 实现 Vercel AI SDK 的 UI 消息流协议，前端直接使用 useChat。
//
// 运行只存在于启动它的进程中：部署多个实例时，同一个对话的请求需要落在同一个实例上。
package ai
