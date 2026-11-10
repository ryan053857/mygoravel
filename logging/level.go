package logging

import (
	"context"
	"log/slog"
)

// componentHandler 按 component 字段选择最低级别：Config.Levels 中有对应项时使用它，否则使用 root。
// component 依次取自 logger.With 的字段、context 字段（WithAttrs）和记录本身的字段；
// 只出现在记录本身时，比 root 更低的级别在 Enabled 阶段已被过滤，无法放宽。
type componentHandler struct {
	inner     slog.Handler
	root      slog.Level
	levels    map[string]slog.Level
	component string // logger.With 设置的 component；WithGroup 之后的字段不计
	grouped   bool
}

func (h *componentHandler) threshold(component string) slog.Level {
	if lv, ok := h.levels[component]; ok {
		return lv
	}
	return h.root
}

func (h *componentHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.threshold(h.componentOf(ctx)) && h.inner.Enabled(ctx, level)
}

func (h *componentHandler) Handle(ctx context.Context, r slog.Record) error {
	if component := h.componentOf(ctx); component == "" {
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == AttrComponent {
				component = a.Value.String()
				return false
			}
			return true
		})
		if r.Level < h.threshold(component) {
			return nil
		}
	}
	return h.inner.Handle(ctx, r)
}

// componentOf 返回 logger.With 设置的 component，其次是 context 中最后设置的 component。
func (h *componentHandler) componentOf(ctx context.Context) string {
	if h.component != "" {
		return h.component
	}
	if ctx == nil {
		return ""
	}
	attrs, _ := ctx.Value(ctxKey{}).([]slog.Attr)
	for i := len(attrs) - 1; i >= 0; i-- {
		if attrs[i].Key == AttrComponent {
			return attrs[i].Value.String()
		}
	}
	return ""
}

func (h *componentHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	c := *h
	c.inner = h.inner.WithAttrs(attrs)
	if !h.grouped {
		for _, a := range attrs {
			if a.Key == AttrComponent {
				c.component = a.Value.String()
			}
		}
	}
	return &c
}

func (h *componentHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.inner = h.inner.WithGroup(name)
	c.grouped = true
	return &c
}
