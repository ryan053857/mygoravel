// Package ai 描述模型的几种能力，以及调用它们所需的配置。
//
// ModelConfig 是 OpenAI 兼容接口上的一个模型，是普通的值。Imager、Speaker、Transcriber、Embedder、Reranker
// 和 Decider 描述生成图片、合成语音、识别语音、计算向量、重排和决策，由 ai/openai、ai/openrouter、
// ai/systemone 等子包实现，测试时使用 ai/aitest 中的替身。
//
// 对话式 agent 在子包 ai/chat 中：后台运行、插话、工具审批，以及把运行事件推送给前端。
// 本包不依赖它，只用到以上能力的应用不会链接 trpc-agent-go。
package ai
