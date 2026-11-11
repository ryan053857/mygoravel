package lock

import (
	"context"
	"sync"
	"time"
)

// MemoryStore 把锁保存在进程内存中，只在同一进程内互斥，适合测试和单实例应用。
// 过期的锁在 Acquire 时清除，每分钟最多扫描一次。
type MemoryStore struct {
	mu    sync.Mutex
	locks map[string]held
	swept time.Time
}

// sweepInterval 是 MemoryStore 清除过期锁的最短间隔。
const sweepInterval = time.Minute

type held struct {
	owner   string
	expires time.Time
}

// NewMemoryStore 创建内存锁存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{locks: make(map[string]held)}
}

// Acquire 实现 Store。
func (s *MemoryStore) Acquire(_ context.Context, name, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if now.Sub(s.swept) >= sweepInterval {
		s.sweep(now)
	}
	if h, ok := s.locks[name]; ok && now.Before(h.expires) {
		return false, nil
	}
	s.locks[name] = held{owner: owner, expires: now.Add(ttl)}
	return true, nil
}

// Refresh 实现 Store。
func (s *MemoryStore) Refresh(_ context.Context, name, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.locks[name]; !ok || h.owner != owner {
		return false, nil
	}
	s.locks[name] = held{owner: owner, expires: time.Now().Add(ttl)}
	return true, nil
}

// Release 实现 Store。
func (s *MemoryStore) Release(_ context.Context, name, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.locks[name]; !ok || h.owner != owner {
		return false, nil
	}
	delete(s.locks, name)
	return true, nil
}

// Held 实现 Store。
func (s *MemoryStore) Held(_ context.Context, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.locks[name]
	return ok && time.Now().Before(h.expires), nil
}

// sweep 删除已过期的锁，例如定时任务按触发时刻取得、从不释放的锁。
func (s *MemoryStore) sweep(now time.Time) {
	for name, h := range s.locks {
		if !now.Before(h.expires) {
			delete(s.locks, name)
		}
	}
	s.swept = now
}
