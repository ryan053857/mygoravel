package lock

import (
	"context"
	"testing"
	"time"
)

func TestMemoryStoreSweepsExpiredLocks(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	for _, name := range []string{"a", "b"} {
		if ok, _ := s.Acquire(ctx, name, "o", time.Millisecond); !ok {
			t.Fatalf("acquire %s", name)
		}
	}
	if ok, _ := s.Acquire(ctx, "live", "o", time.Hour); !ok {
		t.Fatal("acquire live")
	}
	time.Sleep(5 * time.Millisecond)

	// 距离上次扫描不足一分钟时不扫描。
	s.Acquire(ctx, "c", "o", time.Hour)
	if len(s.locks) != 4 {
		t.Fatalf("swept too early: %d locks", len(s.locks))
	}
	s.swept = time.Now().Add(-sweepInterval)
	s.Acquire(ctx, "d", "o", time.Hour)
	if _, ok := s.locks["a"]; ok || len(s.locks) != 3 {
		t.Fatalf("locks after sweep: %v", s.locks)
	}
}
