// Package telemetry 把日志、指标和链路用 OTLP/HTTP 直接推送到一个接收端，例如 OpenObserve，不需要另外部署收集器。
//
// Config 放进应用配置的 telemetry 段，Open 返回三个共用一个 resource 的 Provider：
//
//	tel, err := telemetry.Open(ctx, cfg.Telemetry)
//	if err != nil {
//		return nil, err
//	}
//	stack.MustAdd("telemetry", tel.Shutdown)
//	slog.SetDefault(logging.New(cfg.Logs, logging.Export(tel.Logs)))
//
// 之后把 tel.Meters、tel.Tracers 交给 httpx、queue、schedule 等组件。Endpoint 为空时三个 Provider 都不记录也不推送。
// 框架组件只依赖 OpenTelemetry 的接口，不用这个包、自己创建 Provider 也可以。
package telemetry
