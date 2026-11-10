package ratelimit_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/ratelimit"
)

// testLimiter 检查任意 Limiter 实现的共同行为。newLimiter 以 cfg 创建限流器。
func testLimiter(t *testing.T, newLimiter func(ratelimit.Config) ratelimit.Limiter) {
	ctx := context.Background()

	t.Run("burst then reject per key", func(t *testing.T) {
		l := newLimiter(ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 2})
		for i := range 2 {
			if err := l.Allow(ctx, "a"); err != nil {
				t.Fatalf("call %d: %v", i, err)
			}
		}
		err := l.Allow(ctx, "a")
		if !errors.Is(err, ratelimit.ErrExceeded) || errorx.KindOf(err) != errorx.ResourceExhausted {
			t.Fatalf("third call: %v", err)
		}
		if retry := errorx.RetryAfterOf(err); retry < 59*time.Minute || retry > time.Hour {
			t.Fatalf("retry after=%v", retry)
		}
		if err := l.Allow(ctx, "b"); err != nil {
			t.Fatalf("other key: %v", err)
		}
	})

	t.Run("refills and rejected calls do not consume", func(t *testing.T) {
		l := newLimiter(ratelimit.Config{Rate: 1, Interval: 300 * time.Millisecond, Burst: 1})
		if err := l.Allow(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		var retry time.Duration
		for range 5 {
			err := l.Allow(ctx, "a")
			if !errors.Is(err, ratelimit.ErrExceeded) {
				t.Fatalf("expected rejection, got %v", err)
			}
			retry = errorx.RetryAfterOf(err)
		}
		if retry <= 0 || retry > 300*time.Millisecond {
			t.Fatalf("retry after=%v", retry)
		}
		time.Sleep(retry + 20*time.Millisecond)
		if err := l.Allow(ctx, "a"); err != nil {
			t.Fatalf("after waiting retry-after: %v", err)
		}
	})
}

func TestMemory(t *testing.T) {
	testLimiter(t, func(cfg ratelimit.Config) ratelimit.Limiter { return ratelimit.NewMemory(cfg) })
}

func TestConfigValidate(t *testing.T) {
	for cfg, want := range map[ratelimit.Config]string{
		{Rate: 0, Interval: time.Second, Burst: 1}:         "rate must be positive",
		{Rate: 1, Interval: 0, Burst: 1}:                   "interval must be positive",
		{Rate: 1, Interval: time.Second, Burst: 0}:         "burst must be positive",
		{Rate: 2000, Interval: time.Millisecond, Burst: 1}: "interval / rate must be at least 1µs",
	} {
		if err := cfg.Validate(); err == nil || err.Error() != want {
			t.Fatalf("%+v: %v", cfg, err)
		}
	}
	defer func() {
		if recover() == nil {
			t.Fatal("NewMemory accepted invalid config")
		}
	}()
	ratelimit.NewMemory(ratelimit.Config{})
}
