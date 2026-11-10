package redisbroadcast

import "time"

// SetPingInterval 供测试缩短 PING 间隔，返回恢复函数。
func SetPingInterval(d time.Duration) func() {
	old := pingInterval
	pingInterval = d
	return func() { pingInterval = old }
}
