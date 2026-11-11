package ai

import (
	"context"
	"fmt"
	"log/slog"

	"trpc.group/trpc-go/trpc-agent-go/log"

	"github.com/ryan053857/mygoravel/logging"
)

// SetLogger 让 trpc-agent-go 的日志写到 l，带 component=ai 字段，可以按组件设置级别。
// trpc-agent-go 的日志是进程级的全局设置，在装配时调用一次。
func SetLogger(l *slog.Logger) {
	a := newLogAdapter(l)
	log.Default = a
	log.ContextDefault = a
}

// newLogAdapter 把 l 包装为 trpc-agent-go 的日志接口。
func newLogAdapter(l *slog.Logger) slogAdapter {
	return slogAdapter{logging.Or(l).With(logging.AttrComponent, "ai")}
}

type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) log(level slog.Level, msg string) {
	a.l.Log(context.Background(), level, msg)
}

func (a slogAdapter) Debug(args ...any) { a.log(slog.LevelDebug, fmt.Sprint(args...)) }
func (a slogAdapter) Debugf(format string, args ...any) {
	a.log(slog.LevelDebug, fmt.Sprintf(format, args...))
}
func (a slogAdapter) Info(args ...any) { a.log(slog.LevelInfo, fmt.Sprint(args...)) }
func (a slogAdapter) Infof(format string, args ...any) {
	a.log(slog.LevelInfo, fmt.Sprintf(format, args...))
}
func (a slogAdapter) Warn(args ...any) { a.log(slog.LevelWarn, fmt.Sprint(args...)) }
func (a slogAdapter) Warnf(format string, args ...any) {
	a.log(slog.LevelWarn, fmt.Sprintf(format, args...))
}
func (a slogAdapter) Error(args ...any) { a.log(slog.LevelError, fmt.Sprint(args...)) }
func (a slogAdapter) Errorf(format string, args ...any) {
	a.log(slog.LevelError, fmt.Sprintf(format, args...))
}

// Fatal 和 Fatalf 只记录错误，不退出进程：框架组件不应终止应用。
func (a slogAdapter) Fatal(args ...any) { a.log(slog.LevelError, fmt.Sprint(args...)) }
func (a slogAdapter) Fatalf(format string, args ...any) {
	a.log(slog.LevelError, fmt.Sprintf(format, args...))
}
