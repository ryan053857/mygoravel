package logging

import (
	"context"
	"log/slog"
	"slices"
)

type ctxKey struct{}

// WithAttrs 返回携带 attrs 的新 context；多次调用累加，父 context 不受影响。
func WithAttrs(ctx context.Context, attrs ...slog.Attr) context.Context {
	if len(attrs) == 0 {
		return ctx
	}
	prev, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return context.WithValue(ctx, ctxKey{}, slices.Concat(prev, attrs))
}

// ContextAttrs 返回 ctx 中由 WithAttrs 累加的字段，没有时返回 nil。
func ContextAttrs(ctx context.Context) []slog.Attr {
	attrs, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	return slices.Clone(attrs)
}

// NewContextHandler 包装 h，在处理记录时追加 context 中的字段。
func NewContextHandler(h slog.Handler) slog.Handler {
	if _, ok := h.(contextHandler); ok {
		return h
	}
	return contextHandler{h}
}

type contextHandler struct {
	slog.Handler
}

func (h contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx != nil {
		if attrs, ok := ctx.Value(ctxKey{}).([]slog.Attr); ok {
			r.AddAttrs(attrs...)
		}
	}
	return h.Handler.Handle(ctx, r)
}

func (h contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return contextHandler{h.Handler.WithAttrs(attrs)}
}

func (h contextHandler) WithGroup(name string) slog.Handler {
	return contextHandler{h.Handler.WithGroup(name)}
}
