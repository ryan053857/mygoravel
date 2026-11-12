package logging

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/log"
)

// scopeName 是导出日志的 instrumentation scope。
const scopeName = "github.com/ryan053857/mygoravel/logging"

// Export 把日志同时交给 OpenTelemetry 的 lp，通常是 telemetry.Open 返回的 Logs，由它推送到日志平台。
// 导出的日志和基础输出经过同样的级别、Pipe 和格式化；ctx 中的 span 写入记录的链路字段，
// 日志平台可以从日志跳到链路，trace_id、span_id 不再作为属性重复。服务名和版本由 lp 的 resource 表示。
func Export(lp log.LoggerProvider) Option {
	return func(o *options) { o.export = lp }
}

func newExportHandler(lp log.LoggerProvider, cfg Config) slog.Handler {
	return exportHandler{
		Handler: otelslog.NewHandler(scopeName, otelslog.WithLoggerProvider(lp), otelslog.WithSource(cfg.AddSource)),
		level:   cfg.minLevel(),
	}
}

// exportHandler 按级别过滤后交给 otelslog，并去掉 context 处理器追加在顶层的 trace_id、span_id。
type exportHandler struct {
	slog.Handler
	level   slog.Level
	grouped bool // 已进入分组，记录的属性都在分组内
}

func (h exportHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level && h.Handler.Enabled(ctx, level)
}

func (h exportHandler) Handle(ctx context.Context, r slog.Record) error {
	if !h.grouped && hasRedundant(r) {
		kept := slog.NewRecord(r.Time, r.Level, r.Message, r.PC)
		r.Attrs(func(a slog.Attr) bool {
			if !redundant(a) {
				kept.AddAttrs(a)
			}
			return true
		})
		r = kept
	}
	return h.Handler.Handle(ctx, r)
}

func (h exportHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	h.Handler = h.Handler.WithAttrs(attrs)
	return h
}

func (h exportHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	h.Handler = h.Handler.WithGroup(name)
	h.grouped = true
	return h
}

func hasRedundant(r slog.Record) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = redundant(a)
		return !found
	})
	return found
}

func redundant(a slog.Attr) bool {
	switch a.Key {
	case AttrTraceID, AttrSpanID:
		return true
	}
	return false
}
