package login

import "time"

// SetClock 供测试替换 Realm 的时钟。
func SetClock(r *Realm, now func() time.Time) { r.now = now }
