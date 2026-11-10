package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestMemorySweepsRefilledKeys(t *testing.T) {
	m := NewMemory(Config{Rate: 1, Interval: 20 * time.Millisecond, Burst: 2})
	for _, key := range []string{"a", "b", "c"} {
		if err := m.Allow(context.Background(), key); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if err := m.Allow(context.Background(), "d"); err != nil {
		t.Fatal(err)
	}
	if len(m.tats) != 1 {
		t.Fatalf("refilled keys kept: %v", m.tats)
	}
}
