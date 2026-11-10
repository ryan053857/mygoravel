package lock

import (
	"context"
	"sync"
	"time"
)

// MemoryStore 把锁保存在进程内存中，只在同一进程内互斥，适合测试和单实例应用。
type MemoryStore struct {
	mu    sync.Mutex
	locks map[string]held
}

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
