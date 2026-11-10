package natsbroadcast

import "time"

// SetCheckInterval 供测试缩短检查间隔，返回恢复函数。
func SetCheckInterval(d time.Duration) func() {
	old := checkInterval
	checkInterval = d
	return func() { checkInterval = old }
}
