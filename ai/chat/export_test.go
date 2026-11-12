package chat

import "time"

// SetClusterTiming 供测试调整多实例协作的时间参数，返回恢复函数。
func SetClusterTiming(ping, idle time.Duration) func() {
	oldPing, oldIdle := pingInterval, idleTimeout
	pingInterval, idleTimeout = ping, idle
	return func() { pingInterval, idleTimeout = oldPing, oldIdle }
}

// SetReplyTimeout 供测试缩短等待回执的时间，返回恢复函数。
func SetReplyTimeout(d time.Duration) func() {
	old := replyTimeout
	replyTimeout = d
	return func() { replyTimeout = old }
}

// SetBeforeDone 供测试在 worker 回复 done 之前插入等待，返回恢复函数。
func SetBeforeDone(f func(*Run)) func() {
	old := beforeDone
	beforeDone = f
	return func() { beforeDone = old }
}
