// Package logging 构造 slog.Logger 并连接 context 中的关联字段。
//
// Config 描述级别、格式、时区与 service / version 字段，可直接放进应用配置；
// New 按 Config 返回日志器，入口处通常用 slog.SetDefault 安装：
//
//	slog.SetDefault(logging.New(cfg.Logs,
//		logging.Format(slogformatter.PIIFormatter("user")), // 字段格式化（slog-formatter）
//		logging.Pipe(sampling),                             // Handler 中间件（slog-multi）
//		logging.Fanout(slackHandler),                       // 追加输出
//		logging.Export(tel.Logs),                           // 推送到 OpenTelemetry（telemetry 包）
//	))
//
// 框架组件的 Logger 参数为 nil 时使用 slog.Default()。
// 业务使用 slog 的原生方法，WithAttrs 和 NewContextHandler 负责传递 request_id 等字段。
// 测试中用 logtest 捕获日志。
package logging
