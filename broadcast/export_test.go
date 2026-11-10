package broadcast

import "time"

// SetTimings 供测试缩短轮询间隔、空号等待时间和保留时间。
func SetTimings(d *Database, interval, gapTimeout, retention time.Duration) {
	d.interval, d.gapTimeout, d.retention = interval, gapTimeout, retention
}
