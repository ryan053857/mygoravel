package ai

import "time"

// SetClusterTiming 供测试调整多实例协作的时间参数，返回恢复函数。
func SetClusterTiming(ping, idle time.Duration) func() {
	oldPing, oldIdle := pingInterval, idleTimeout
	pingInterval, idleTimeout = ping, idle
	return func() { pingInterval, idleTimeout = oldPing, oldIdle }
}
