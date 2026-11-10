package ratelimit

import (
	"context"
	"sync"
	"time"
)

// Memory 在进程内存中计数，多实例部署时每个实例分别计数。
//
// 已回满的 key 与从未出现过的 key 等价，Memory 在调用 Allow 时顺带删除它们，
// 因此内存只与最近一个 Burst 周期内活跃的 key 数有关，不需要另行清理。
type Memory struct {
	gcra

	mu    sync.Mutex
	tats  map[string]time.Time // 每个 key 的理论到达时间
	swept time.Time
}

// NewMemory 创建进程内限流器，cfg 不合法时 panic。
func NewMemory(cfg Config) *Memory {
	return &Memory{gcra: newGCRA("NewMemory", cfg), tats: map[string]time.Time{}, swept: time.Now()}
}

// Allow 实现 Limiter，不会返回存储错误。
func (m *Memory) Allow(_ context.Context, key string) error {
	now := time.Now()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(now)
	next, wait := m.take(m.tats[key], now)
	if wait > 0 {
		return exceeded(wait)
	}
	m.tats[key] = next
	return nil
}

// sweep 每隔一个 Burst 周期删除已回满的 key，摊销后每次调用为 O(1)。
func (m *Memory) sweep(now time.Time) {
	if now.Sub(m.swept) < m.ahead+m.step {
		return
	}
	for key, tat := range m.tats {
		if !tat.After(now) {
			delete(m.tats, key)
		}
	}
	m.swept = now
}
