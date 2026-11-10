package ratelimit_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/ratelimit"
)

func TestDatabase(t *testing.T) {
	forEachDriver(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		n := 0
		testLimiter(t, func(cfg ratelimit.Config) ratelimit.Limiter {
			n++
			return ratelimit.NewDatabase(db, fmt.Sprint("test", n), cfg)
		})
	})
}

func TestDatabaseNamesAreIsolated(t *testing.T) {
	forEachDriver(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := openDB(t, driver)
		cfg := ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 1}
		login, reset := ratelimit.NewDatabase(db, "login", cfg), ratelimit.NewDatabase(db, "reset", cfg)
		if err := login.Allow(ctx, "alice"); err != nil {
			t.Fatal(err)
		}
		if err := reset.Allow(ctx, "alice"); err != nil {
			t.Fatalf("names must not share quota: %v", err)
		}
		if err := ratelimit.NewDatabase(db, "login", cfg).Allow(ctx, "alice"); err == nil {
			t.Fatal("same name must share quota across instances")
		}
	})
}

func TestDatabaseConcurrentCallsAreExact(t *testing.T) {
	forEachDriver(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		l := ratelimit.NewDatabase(openDB(t, driver), "burst", ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 5})
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
	})
}

func TestDatabaseCleanup(t *testing.T) {
	forEachDriver(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := openDB(t, driver)
		short := ratelimit.NewDatabase(db, "short", ratelimit.Config{Rate: 1, Interval: 10 * time.Millisecond, Burst: 1})
		long := ratelimit.NewDatabase(db, "long", ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 1})
		for _, key := range []string{"a", "b", "c"} {
			if err := short.Allow(ctx, key); err != nil {
				t.Fatal(err)
			}
		}
		if err := long.Allow(ctx, "a"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(30 * time.Millisecond)
		if _, err := long.Cleanup(ctx, 0); err == nil {
			t.Fatal("expected error for non-positive limit")
		}
		if n, err := long.Cleanup(ctx, 2); n != 2 || err != nil {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := long.Cleanup(ctx, 10); n != 1 || err != nil {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		if err := long.Allow(ctx, "a"); err == nil {
			t.Fatal("cleanup removed a row that still limits")
		}
	})
}
