//go:build integration

package lock_test

import (
	"context"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/lock"
)

var _ lock.Store = (*lock.DBStore)(nil)

func TestDBStore(t *testing.T) {
	db := dbtest.Open(t, database.Postgres, lock.Migrations())
	ctx := context.Background()
	testStore(t, func() lock.Store {
		if _, err := db.NewTruncateTable().Table("locks").Exec(ctx); err != nil {
			t.Fatal(err)
		}
		return lock.NewDBStore(db)
	})
}

func TestDBStoreCleanup(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t, database.Postgres, lock.Migrations())
	store := lock.NewDBStore(db)
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
}
