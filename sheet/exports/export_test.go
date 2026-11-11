package exports

import "time"

// SetSaveInterval 修改两次保存进度之间的最短时间，返回恢复原值的函数。
func SetSaveInterval(d time.Duration) (restore func()) {
	old := saveInterval
	saveInterval = d
	return func() { saveInterval = old }
}
