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

	t.Run("reset refills one key", func(t *testing.T) {
		l := newLimiter(ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 1})
		for _, key := range []string{"a", "b"} {
			if err := l.Allow(ctx, key); err != nil {
				t.Fatal(err)
			}
		}
		if err := l.Reset(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		if err := l.Reset(ctx, "never seen"); err != nil {
			t.Fatalf("reset unknown key: %v", err)
		}
		if err := l.Allow(ctx, "a"); err != nil {
			t.Fatalf("after reset: %v", err)
		}
		if err := l.Allow(ctx, "b"); !errors.Is(err, ratelimit.ErrExceeded) {
			t.Fatalf("reset affected another key: %v", err)
		}
	})

	t.Run("refills and rejected calls do not consume", func(t *testing.T) {
		// 结果取决于调用耗时：一组调用接近 interval 时，
		// 无法区分配额回满和实现错误，放大 interval 重新检查。
		for interval := 300 * time.Millisecond; !refillsWithoutConsuming(t, newLimiter(ratelimit.Config{Rate: 1, Interval: interval, Burst: 1}), interval); interval *= 4 {
			if interval > 5*time.Second {
				t.Fatal("calls are too slow to check refill timing")
			}
			t.Logf("calls took too long for interval %v, retrying with a longer interval", interval)
		}
	})
}

// refillsWithoutConsuming 检查容量为 1 的限流器 l：取得配额后的连续调用都被拒绝且不消耗配额，
// 等待 Retry-After 之后配额回满。调用耗时超过 interval 的 3/4、结果不能说明问题时返回 false。
func refillsWithoutConsuming(t *testing.T, l ratelimit.Limiter, interval time.Duration) bool {
	t.Helper()
	ctx := context.Background()
	start := time.Now()
	if err := l.Allow(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	tooSlow := func() bool { return time.Since(start) >= interval*3/4 }
	var retry time.Duration
	for range 5 {
		err := l.Allow(ctx, "a")
		if tooSlow() {
			return false
		}
		if !errors.Is(err, ratelimit.ErrExceeded) {
			t.Fatalf("expected rejection %v after the first call, got %v", time.Since(start), err)
		}
		retry = errorx.RetryAfterOf(err)
	}
	if retry <= 0 || retry > interval {
		t.Fatalf("retry after=%v, interval=%v", retry, interval)
	}
	time.Sleep(retry + 20*time.Millisecond)
	if err := l.Allow(ctx, "a"); err != nil {
		t.Fatalf("after waiting retry-after: %v", err)
	}
	return true
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
