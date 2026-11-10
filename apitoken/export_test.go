package apitoken

import "time"

// SetClock 供测试替换 Store 的时钟。
func SetClock(s *Store, now func() time.Time) { s.now = now }
