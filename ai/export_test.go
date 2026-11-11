package ai

import (
	"log/slog"
	"time"

	"trpc.group/trpc-go/trpc-agent-go/log"
)

// NewLogAdapter 是 SetLogger 安装的适配器。测试直接使用它，不修改 trpc-agent-go 的全局日志：
// 同一个包中其他测试启动的运行，可能还有 trpc-agent-go 内部的 goroutine 在写日志。
func NewLogAdapter(l *slog.Logger) log.Logger { return newLogAdapter(l) }

// SetClusterTiming 供测试调整多实例协作的时间参数，返回恢复函数。
func SetClusterTiming(ping, idle time.Duration) func() {
	oldPing, oldIdle := pingInterval, idleTimeout
	pingInterval, idleTimeout = ping, idle
	return func() { pingInterval, idleTimeout = oldPing, oldIdle }
}
