// Package logging 构造 slog.Logger 并连接 context 中的关联字段。
//
// Config 描述级别与格式，可直接放进应用配置；New 按 Config 返回独立日志器。
// 业务使用 slog 的原生方法，WithAttrs 和 NewContextHandler 负责传递 request_id 等字段。
package logging
