package lock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-tenon/tenon/lock"
	"github.com/go-tenon/tenon/lock/locktest"
)

func TestAcquireValidation(t *testing.T) {
	locker := lock.New(locktest.NewStore())
	ctx := context.Background()
	if _, err := locker.Acquire(ctx, "", time.Second); err == nil {
		t.Fatal("expected error for empty name")
	}
	if _, err := locker.Acquire(ctx, "a", 0); err == nil {
		t.Fatal("expected error for non-positive ttl")
	}
}

func TestWaitHonorsContext(t *testing.T) {
	locker := lock.New(locktest.NewStore())
	if _, err := locker.Acquire(context.Background(), "a", time.Minute); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := locker.Wait(ctx, "a", time.Minute, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
}

// testStore 是所有 Store 实现共同遵守的契约，集成测试用它验证 Database。
