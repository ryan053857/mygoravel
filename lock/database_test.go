package lock_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
)

var _ lock.Store = (*lock.Database)(nil)

func TestDatabase(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, lock.Migrations())
		ctx := context.Background()
		locktest.TestStore(t, func() lock.Store {
			if _, err := db.NewTruncateTable().Table("locks").Exec(ctx); err != nil {
				t.Fatal(err)
			}
			return lock.NewDatabase(db)
		})
	})
}

func TestDatabaseCleanup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := dbtesting.Open(t, driver, lock.Migrations())
		store := lock.NewDatabase(db)
		for _, name := range []string{"x1", "x2", "x3"} {
			if ok, err := store.Acquire(ctx, name, "o", time.Millisecond); !ok || err != nil {
				t.Fatalf("acquire %s ok=%v err=%v", name, ok, err)
			}
		}
		if ok, err := store.Acquire(ctx, "live", "o", time.Hour); !ok || err != nil {
			t.Fatalf("acquire live ok=%v err=%v", ok, err)
		}
		time.Sleep(10 * time.Millisecond)
		if _, err := store.Cleanup(ctx, 0); err == nil {
			t.Fatal("expected error for non-positive limit")
		}
		if n, err := store.Cleanup(ctx, 2); n != 2 || err != nil {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := store.Cleanup(ctx, 10); n != 1 || err != nil {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		if ok, _ := store.Acquire(ctx, "live", "other", time.Hour); ok {
			t.Fatal("live lock removed")
		}
	})
}

func TestDatabaseRejectsLongNames(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		store := lock.NewDatabase(dbtesting.Open(t, driver, lock.Migrations()))
		ctx := context.Background()
		if ok, err := store.Acquire(ctx, strings.Repeat("n", lock.MaxNameLength), "o", time.Minute); !ok || err != nil {
			t.Fatalf("max length ok=%v err=%v", ok, err)
		}
		if ok, err := store.Acquire(ctx, strings.Repeat("n", lock.MaxNameLength+1), "o", time.Minute); ok || err == nil {
			t.Fatalf("over length ok=%v err=%v", ok, err)
		}
	})
}
