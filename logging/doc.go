// Package logging 构造 slog.Logger 并连接 context 中的关联字段。
//
// Config 描述级别、格式与 service / version 字段，可直接放进应用配置；
// New 按 Config 返回日志器，入口处通常用 slog.SetDefault 安装：
//
//	slog.SetDefault(logging.New(cfg.Logs))
//
// 框架组件的 Logger 参数为 nil 时使用 slog.Default()。
// 业务使用 slog 的原生方法，WithAttrs 和 NewContextHandler 负责传递 request_id 等字段。
package logging
