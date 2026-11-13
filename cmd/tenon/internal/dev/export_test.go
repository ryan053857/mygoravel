package dev

import "time"

// SetPollInterval 修改检查 Go 源文件的间隔，返回恢复原值的函数。
func SetPollInterval(d time.Duration) func() {
	old := pollInterval
	pollInterval = d
	return func() { pollInterval = old }
}
