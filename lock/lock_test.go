package lock_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/lock"
)

var _ lock.Store = (*lock.MemoryStore)(nil)

func TestMemoryStore(t *testing.T) {
	testStore(t, func() lock.Store { return lock.NewMemoryStore() })
}

func TestAcquireValidation(t *testing.T) {
	locker := lock.New(lock.NewMemoryStore())
	ctx := context.Background()
	if _, err := locker.Acquire(ctx, "", time.Second); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := locker.Acquire(ctx, "a", 0); err == nil {
		t.Fatal("expected error for non-positive ttl")
	}
}

func TestWaitHonorsContext(t *testing.T) {
	locker := lock.New(lock.NewMemoryStore())
	if _, err := locker.Acquire(context.Background(), "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := locker.Wait(ctx, "a", time.Minute, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

// testStore 是所有 Store 实现共同遵守的契约，集成测试用它验证 DBStore。
func testStore(t *testing.T, newStore func() lock.Store) {
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
		lk, err := locker.Acquire(ctx, "a", 50*time.Millisecond)
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
		lk, err := locker.Acquire(ctx, "a", 30*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		if err := lk.Refresh(ctx, time.Minute); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
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
		time.AfterFunc(50*time.Millisecond, func() { lk.Release(ctx) })
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
