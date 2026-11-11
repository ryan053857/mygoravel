// Package locktest 提供测试用的内存锁存储，以及 lock.Store 实现的契约测试。
package locktest

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/lock"
)

var _ lock.Store = (*Store)(nil)

// Store 把锁保存在进程内存中，只在同一进程内互斥，供测试使用：生产环境中，
// 不停机升级和多实例部署时同时运行的进程需要共用 lock.Database 或 Redis 存储。
// 过期的锁在 Acquire 时清除，每分钟最多扫描一次。
type Store struct {
	mu    sync.Mutex
	locks map[string]held
	swept time.Time
}

// sweepInterval 是 Store 清除过期锁的最短间隔。
const sweepInterval = time.Minute

type held struct {
	owner   string
	expires time.Time
}

// NewStore 创建内存锁存储。
func NewStore() *Store {
	return &Store{locks: make(map[string]held)}
}

// Acquire 实现 lock.Store。
func (s *Store) Acquire(_ context.Context, name, owner string, ttl time.Duration) (bool, error) {
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

// Refresh 实现 lock.Store。
func (s *Store) Refresh(_ context.Context, name, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.locks[name]; !ok || h.owner != owner {
		return false, nil
	}
	s.locks[name] = held{owner: owner, expires: time.Now().Add(ttl)}
	return true, nil
}

// Release 实现 lock.Store。
func (s *Store) Release(_ context.Context, name, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.locks[name]; !ok || h.owner != owner {
		return false, nil
	}
	delete(s.locks, name)
	return true, nil
}

// Held 实现 lock.Store。
func (s *Store) Held(_ context.Context, name string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.locks[name]
	return ok && time.Now().Before(h.expires), nil
}

// sweep 删除已过期的锁，例如定时任务按触发时刻取得、从不释放的锁。
func (s *Store) sweep(now time.Time) {
	for name, h := range s.locks {
		if !now.Before(h.expires) {
			delete(s.locks, name)
		}
	}
	s.swept = now
}

// TestStore 检查 lock.Store 实现的共同行为。newStore 每次返回一个新的、互不影响的存储。
func TestStore(t *testing.T, newStore func() lock.Store) {
	ctx := context.Background()

	t.Run("exclusive", func(t *testing.T) {
		locker := lock.New(newStore())
		first, err := locker.Acquire(ctx, "a", time.Minute)
		if err != nil || first.Name() != "a" {
			t.Fatalf("first=%v err=%v", first, err)
		}
		if _, err := locker.Acquire(ctx, "a", time.Minute); !errors.Is(err, lock.ErrNotAcquired) {
			t.Fatalf("second acquire err=%v", err)
		}
		if _, err := locker.Acquire(ctx, "b", time.Minute); err != nil {
			t.Fatalf("other name err=%v", err)
		}
		if err := first.Release(ctx); err != nil {
			t.Fatal(err)
		}
		if err := first.Release(ctx); !errors.Is(err, lock.ErrNotHeld) {
			t.Fatalf("double release err=%v", err)
		}
		if _, err := locker.Acquire(ctx, "a", time.Minute); err != nil {
			t.Fatalf("acquire after release err=%v", err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		locker := lock.New(newStore())
		stale, err := locker.Acquire(ctx, "a", 20*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
		fresh, err := locker.Acquire(ctx, "a", time.Minute)
		if err != nil {
			t.Fatalf("acquire expired err=%v", err)
		}
		// 过期的持有者不能续期或释放新持有者的锁。
		if err := stale.Refresh(ctx, time.Minute); !errors.Is(err, lock.ErrNotHeld) {
			t.Fatalf("stale refresh err=%v", err)
		}
		if err := stale.Release(ctx); !errors.Is(err, lock.ErrNotHeld) {
			t.Fatalf("stale release err=%v", err)
		}
		if _, err := locker.Acquire(ctx, "a", time.Minute); !errors.Is(err, lock.ErrNotAcquired) {
			t.Fatalf("fresh lock lost err=%v", err)
		}
		if err := fresh.Release(ctx); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("held", func(t *testing.T) {
		locker := lock.New(newStore())
		held := func(want bool) {
			t.Helper()
			if got, err := locker.Held(ctx, "a"); err != nil || got != want {
				t.Fatalf("held=%v err=%v, want %v", got, err, want)
			}
		}
		held(false)
		lk, err := locker.Acquire(ctx, "a", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		held(true)
		if err := lk.Release(ctx); err != nil {
			t.Fatal(err)
		}
		held(false)
		if _, err := locker.Acquire(ctx, "a", 30*time.Millisecond); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
		held(false) // 过期
	})

	t.Run("refresh", func(t *testing.T) {
		locker := lock.New(newStore())
		// 初始的过期时间要足够续期在慢的机器上完成，之后等它过去，确认续期生效。
		lk, err := locker.Acquire(ctx, "a", 250*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if err := lk.Refresh(ctx, time.Minute); err != nil {
			t.Fatal(err)
		}
		time.Sleep(300 * time.Millisecond)
		if _, err := locker.Acquire(ctx, "a", time.Minute); !errors.Is(err, lock.ErrNotAcquired) {
			t.Fatalf("refreshed lock lost err=%v", err)
		}
	})

	t.Run("wait", func(t *testing.T) {
		locker := lock.New(newStore())
		lk, err := locker.Acquire(ctx, "a", time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := locker.Wait(ctx, "a", time.Minute, 50*time.Millisecond); !errors.Is(err, lock.ErrNotAcquired) {
			t.Fatalf("wait timeout err=%v", err)
		}
		time.AfterFunc(50*time.Millisecond, func() { _ = lk.Release(ctx) })
		if _, err := locker.Wait(ctx, "a", time.Minute, 5*time.Second); err != nil {
			t.Fatalf("wait after release err=%v", err)
		}
	})

	t.Run("do", func(t *testing.T) {
		locker := lock.New(newStore())
		boom := errors.New("boom")
		err := locker.Do(ctx, "a", time.Minute, func(ctx context.Context) error {
			if err := locker.Do(ctx, "a", time.Minute, func(context.Context) error { return nil }); !errors.Is(err, lock.ErrNotAcquired) {
				t.Errorf("nested do err=%v", err)
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("do err=%v", err)
		}
		// fn 返回后锁已释放。
		if err := locker.Do(ctx, "a", time.Minute, func(context.Context) error { return nil }); err != nil {
			t.Fatalf("do after release err=%v", err)
		}
		// fn 超过 ttl 且锁被他人取得时报告 ErrNotHeld。
		err = locker.Do(ctx, "a", 20*time.Millisecond, func(ctx context.Context) error {
			time.Sleep(50 * time.Millisecond)
			_, err := locker.Acquire(ctx, "a", time.Minute)
			return err
		})
		if !errors.Is(err, lock.ErrNotHeld) {
			t.Fatalf("overrun do err=%v", err)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		store := newStore()
		var wg sync.WaitGroup
		var won atomic.Int32
		for range 20 {
			wg.Go(func() {
				_, err := lock.New(store).Acquire(ctx, "a", time.Minute)
				switch {
				case err == nil:
					won.Add(1)
				case !errors.Is(err, lock.ErrNotAcquired):
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatalf("winners=%d", won.Load())
		}
	})
}
