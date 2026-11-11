// Package ai 在 trpc-agent-go 之上运行对话式 agent：后台运行、插话、工具审批，以及把运行事件推送给客户端。
//
// agent、模型、工具和会话存储都直接使用 trpc-agent-go 的类型；本包只补上 Web 应用需要的部分：
//
//   - Runs 让一次运行脱离 HTTP 请求，在后台跑完，客户端断线后用 Last-Event-ID 接上；
//   - Run.Steer 在运行中途插入用户消息，在当前这批工具调用结束后生效，不会丢失；
//   - RequireApproval 让指定的工具停下等待审批，Runs.Approve 和 Runs.Reject 在任意进程中继续；
//   - UIStream、Submit、UIMessages 实现 Vercel AI SDK 的 UI 消息流协议，前端直接使用 useChat。
//
// 此外，Imager、Speaker、Transcriber 和 Embedder 描述生成图片、合成语音、识别语音和计算向量这几种能力，
// 由 ai/openai、ai/openrouter 等子包实现，测试时使用 ai/aitest 中的替身。
//
// 运行在启动它的实例上执行；部署多个实例时用 Cluster 让各实例协作，同一个对话的请求可以落在任何实例上。
package ai
