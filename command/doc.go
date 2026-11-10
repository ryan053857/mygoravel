// Package command 用 Cobra 构造 HTTP API 应用的命令行。
// 应用通过 App 声明配置类型、路由、服务入口和迁移，框架实现 serve、openapi、routes、
// migrate 和 config 命令，以及配置加载、信号处理和退出码。
package command
