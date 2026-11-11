// Package command 用 Cobra 构造应用的命令行。
// Root 创建根命令，应用用 AddCommand 显式装配需要的命令：Run（加载配置后执行，
// 适合 serve、worker）、Migrate、Config、OpenAPI 和 Routes，也可以加入任意 Cobra 命令。
// Bind 声明执行 action 的命令，依赖推迟到命令执行时才装配。
// Concurrently 在一个进程中同时运行 HTTP 服务、队列 worker 等多个常驻部分。
package command
