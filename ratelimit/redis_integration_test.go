package ratelimit_test

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/internal/redistest"
	"github.com/ryan053857/mygoravel/ratelimit"
	"github.com/ryan053857/mygoravel/ratelimit/redislimit"
)

// 每个限流器使用随机 name，测试之间的键互不影响，不需要清空 Redis。
func testName() string { return "test:ratelimit:" + rand.Text() }

func TestRedis(t *testing.T) {
	rdb := redistest.Open(t)
	testLimiter(t, func(cfg ratelimit.Config) ratelimit.Limiter {
		return redislimit.New(rdb, testName(), cfg)
	})
}

func TestRedisNamesAreIsolated(t *testing.T) {
	ctx := context.Background()
	rdb := redistest.Open(t)
	cfg := ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 1}
	name := testName()
	login, reset := redislimit.New(rdb, name, cfg), redislimit.New(rdb, testName(), cfg)
	if err := login.Allow(ctx, "alice"); err != nil {
		t.Fatal(err)
	}
	if err := reset.Allow(ctx, "alice"); err != nil {
		t.Fatalf("names must not share quota: %v", err)
	}
	if err := redislimit.New(rdb, name, cfg).Allow(ctx, "alice"); err == nil {
		t.Fatal("same name must share quota across instances")
	}
}

func TestRedisConcurrentCallsAreExact(t *testing.T) {
	ctx := context.Background()
	l := redislimit.New(redistest.Open(t), testName(), ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 5})
	results := make(chan error, 20)
	for range cap(results) {
		go func() { results <- l.Allow(ctx, "k") }()
	}
	allowed := 0
	for range cap(results) {
		if <-results == nil {
			allowed++
		}
	}
	if allowed != 5 {
		t.Fatalf("allowed=%d, want 5", allowed)
	}
}

func TestRedisKeyExpiresWhenRefilled(t *testing.T) {
	ctx := context.Background()
	rdb := redistest.Open(t)
	name := testName()
	l := redislimit.New(rdb, name, ratelimit.Config{Rate: 1, Interval: time.Minute, Burst: 3})
	for range 2 {
		if err := l.Allow(ctx, "k"); err != nil {
			t.Fatal(err)
		}
	}
	// 用掉 2 个配额，2 分钟后回满。
	if ms, _ := rdb.Do(ctx, rdb.B().Pttl().Key(name+":k").Build()).AsInt64(); ms < 119_000 || ms > 120_000 {
		t.Fatalf("ttl=%dms", ms)
	}
}
