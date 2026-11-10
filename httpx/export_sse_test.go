package httpx

import "time"

// SetHeartbeatInterval 供测试缩短 SSE 心跳间隔，返回恢复函数。
func SetHeartbeatInterval(d time.Duration) func() {
	old := heartbeatInterval
	heartbeatInterval = d
	return func() { heartbeatInterval = old }
}
