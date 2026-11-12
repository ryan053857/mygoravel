package monitor

import "time"

// SetNow 替换 m 读取当前时间的函数。
func SetNow(m *Monitor, now func() time.Time) { m.now = now }

// SetFlushInterval 修改之后创建的 Monitor 定期写入的间隔，返回恢复原值的函数。
func SetFlushInterval(d time.Duration) (restore func()) {
	old := flushInterval
	flushInterval = d
	return func() { flushInterval = old }
}
