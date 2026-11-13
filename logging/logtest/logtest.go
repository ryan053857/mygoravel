// Package logtest 在测试中捕获日志并提供断言辅助。
//
//	rec := logtest.Capture(t) // 测试期间替换 slog.Default
//	// ……执行被测代码
//	if !rec.Has(slog.LevelError, "request failed") { t.Fatal(rec.Records()) }
//	status := rec.Find("http request").Attr("response.status")
//
// Capture 修改全局状态，使用它的测试不能 t.Parallel()；并行测试改用 New，
// 并把 Recorder.Logger() 显式传给被测组件。
package logtest

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/go-tenon/tenon/logging"
)

// Record 是一条捕获的日志。
type Record struct {
	Time    time.Time
	Level   slog.Level
	Message string
	// Attrs 是展开后的字段，分组以 . 连接，例如 response.status；值为 slog.Value.Any() 的结果。
	Attrs map[string]any
}

// Attr 返回字段值，不存在时返回 nil。
func (r Record) Attr(key string) any { return r.Attrs[key] }

// Recorder 记录日志，可并发使用。
type Recorder struct {
	mu      sync.Mutex
	records []Record
}

// New 返回空的 Recorder。
func New() *Recorder { return &Recorder{} }

// Capture 返回 Recorder 并在测试期间把它安装为 slog.Default，测试结束后恢复。
func Capture(t testing.TB) *Recorder {
	t.Helper()
	r := New()
	prev := slog.Default()
	slog.SetDefault(r.Logger())
	t.Cleanup(func() { slog.SetDefault(prev) })
	return r
}

// Logger 返回写入 r 的日志器，与 logging.New 一样会输出 context 字段并展开 error。
// 记录所有级别。
func (r *Recorder) Logger() *slog.Logger {
	return logging.Or(slog.New(&handler{rec: r}))
}

// Records 返回已捕获日志的副本。
func (r *Recorder) Records() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.records)
}

// Find 返回第一条消息为 msg 的日志；不存在时第二个返回值为 false。
func (r *Recorder) Find(msg string) (Record, bool) {
	for _, rec := range r.Records() {
		if rec.Message == msg {
			return rec, true
		}
	}
	return Record{}, false
}

// Has 报告是否捕获了级别为 level、消息为 msg 的日志。
func (r *Recorder) Has(level slog.Level, msg string) bool {
	return slices.ContainsFunc(r.Records(), func(rec Record) bool {
		return rec.Level == level && rec.Message == msg
	})
}

// Reset 清空已捕获的日志。
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = nil
}

type handler struct {
	rec    *Recorder
	attrs  map[string]any
	prefix string
}

func (h *handler) Enabled(context.Context, slog.Level) bool { return true }

func (h *handler) Handle(_ context.Context, r slog.Record) error {
	attrs := make(map[string]any, len(h.attrs)+r.NumAttrs())
	for k, v := range h.attrs {
		attrs[k] = v
	}
	r.Attrs(func(a slog.Attr) bool {
		flatten(attrs, h.prefix, a)
		return true
	})
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	h.rec.records = append(h.rec.records, Record{Time: r.Time, Level: r.Level, Message: r.Message, Attrs: attrs})
	return nil
}

func (h *handler) WithAttrs(as []slog.Attr) slog.Handler {
	attrs := make(map[string]any, len(h.attrs)+len(as))
	for k, v := range h.attrs {
		attrs[k] = v
	}
	for _, a := range as {
		flatten(attrs, h.prefix, a)
	}
	return &handler{rec: h.rec, attrs: attrs, prefix: h.prefix}
}

func (h *handler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &handler{rec: h.rec, attrs: h.attrs, prefix: h.prefix + name + "."}
}

// flatten 按 slog 的规则展开字段：空键的分组内联，空分组忽略。
func flatten(dst map[string]any, prefix string, a slog.Attr) {
	v := a.Value.Resolve()
	if v.Kind() != slog.KindGroup {
		if a.Key != "" {
			dst[prefix+a.Key] = v.Any()
		}
		return
	}
	if a.Key != "" {
		prefix += a.Key + "."
	}
	for _, g := range v.Group() {
		flatten(dst, prefix, g)
	}
}
