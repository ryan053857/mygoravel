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

// TestDatabaseReadsClockAfterLock 检查 Allow 在取得锁之后才读取当前时间：
// 调用等锁期间，另一个事务把 tat 推到“此刻刚好用完配额”，调用拿到锁时已经晚于此刻，应当放行。
// 若在等锁之前读取时间，调用会用更早的时间判断，被错误地拒绝。
func TestDatabaseReadsClockAfterLock(t *testing.T) {
	forEachDriver(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := openDB(t, driver)
		l := ratelimit.NewDatabase(db, "clock", ratelimit.Config{Rate: 1, Interval: time.Hour, Burst: 2})
		if err := l.Allow(ctx, "k"); err != nil {
			t.Fatal(err)
		}

		tx, err := db.BeginTx(ctx, nil) // SQLite 在 BEGIN 时取得写锁
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		sqlite := driver.Name() == "sqlite"
		if !sqlite {
			if _, err := tx.ExecContext(ctx, `SELECT 1 FROM rate_limits WHERE name = 'clock' AND key = 'k' FOR UPDATE`); err != nil {
				t.Fatal(err)
			}
		}

		result := make(chan error, 1)
		go func() { result <- l.Allow(ctx, "k") }()
		time.Sleep(100 * time.Millisecond)
		select {
		case err := <-result:
			t.Fatalf("Allow did not wait for the lock: %v", err)
		default:
		}

		// tat = 此刻 + ahead：此刻之后的调用都应放行。
		if sqlite {
			_, err = tx.ExecContext(ctx, `UPDATE rate_limits SET tat = ? WHERE name = 'clock' AND key = 'k'`,
				time.Now().Add(time.Hour).UnixMicro())
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE rate_limits SET tat = clock_timestamp() + interval '1 hour' WHERE name = 'clock' AND key = 'k'`)
		}
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		if err := <-result; err != nil {
			t.Fatalf("call that got the lock later was rejected: %v", err)
		}
	})
}
