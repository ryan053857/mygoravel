// Package chat 在 trpc-agent-go 之上运行对话式 agent：后台运行、插话、工具审批，以及把运行事件推送给客户端。
//
// agent、模型、工具和会话存储都直接使用 trpc-agent-go 的类型；本包只补上 Web 应用需要的部分：
//
//   - Runs 让一次运行脱离 HTTP 请求，在后台跑完，客户端断线后用 Last-Event-ID 接上；
//   - Run.Steer 在运行中途插入用户消息，在当前这批工具调用结束后生效，不会丢失；
//   - RequireApproval 让指定的工具停下等待审批，Runs.Approve 和 Runs.Reject 在任意进程中继续；
//   - UIStream、Submit、UIMessages 实现 Vercel AI SDK 的 UI 消息流协议，前端直接使用 useChat。
//
// 对话默认保存在 trpc-agent-go 的会话存储中，由 NewSessions 创建，保存在应用的 PostgreSQL 中。
// 已经有自己消息表的产品（群聊、客服会话、外部渠道）用 UseHistory 让 Runs 从应用的表读取上下文、
// 把产出交给应用保存，用 Runs.Notify 在有新消息时触发运行。
//
// NewModel 按 ai.ModelConfig 创建模型；测试时使用 chattest 中按脚本回复的模型。
//
// 运行在启动它的实例上执行；部署多个实例时用 Cluster 让各实例协作，同一个对话的请求可以落在任何实例上。
package chat
