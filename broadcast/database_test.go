package broadcast_test

import (
	"context"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

func fastDatabase(db bun.IDB, gapTimeout time.Duration) *broadcast.Database {
	d := broadcast.NewDatabase(db, nil)
	broadcast.SetTimings(d, 10*time.Millisecond, gapTimeout, time.Hour)
	return d
}

func TestDatabase(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		broadcasttest.TestBackend(t, func(t *testing.T) (broadcast.Backend, broadcast.Backend) {
			db := dbtesting.Open(t, driver, broadcast.Migrations())
			return fastDatabase(db, time.Second), fastDatabase(db, time.Second)
		})
	})
}

// 自增 id 先分配后提交：未提交的较小 id 在等待时间内提交时按顺序投递，回滚时超时后跳过。
func TestDatabaseWaitsForUncommittedIDs(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		if driver.Name() == "sqlite" {
			t.Skip("SQLite 只有一个写入者，id 按分配顺序提交")
		}
		ctx := context.Background()
		db := dbtesting.Open(t, driver, broadcast.Migrations())
		h := broadcast.New(fastDatabase(db, 500*time.Millisecond), nil)
		defer h.Close()
		sub := h.Subscribe("room:1")
		broadcasttest.Ready(t, h)
		publisher := fastDatabase(db, 0)

		for _, commit := range []bool{true, false} {
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := fastDatabase(tx, 0).Publish(ctx, broadcast.Message{Topic: "room:1", Data: []byte("in tx")}); err != nil {
				t.Fatal(err)
			}
			if err := publisher.Publish(ctx, broadcast.Message{Topic: "room:1", Data: []byte("after")}); err != nil {
				t.Fatal(err)
			}
			select {
			case m := <-sub.C():
				t.Fatalf("delivered %q past an uncommitted id", m.Data)
			case <-time.After(200 * time.Millisecond):
			}
			if commit {
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				if got := broadcasttest.Receive(t, sub); string(got.Data) != "in tx" {
					t.Fatalf("first = %q", got.Data)
				}
			} else {
				_ = tx.Rollback()
			}
			if got := broadcasttest.Receive(t, sub); string(got.Data) != "after" {
				t.Fatalf("next = %q", got.Data)
			}
		}
	})
}

func TestDatabaseCleanup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := dbtesting.Open(t, driver, broadcast.Migrations())
		d := broadcast.NewDatabase(db, nil)
		for range 5 {
			if err := d.Publish(ctx, broadcast.Message{Topic: "room:1"}); err != nil {
				t.Fatal(err)
			}
		}
		if n, err := d.Cleanup(ctx, 10); err != nil || n != 0 {
			t.Fatalf("fresh rows removed: n=%d err=%v", n, err)
		}
		broadcast.SetTimings(d, time.Second, time.Second, -time.Second) // 所有行都已过保留期
		if n, err := d.Cleanup(ctx, 3); err != nil || n != 3 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if n, err := d.Cleanup(ctx, 3); err != nil || n != 2 {
			t.Fatalf("n=%d err=%v", n, err)
		}
		if _, err := d.Cleanup(ctx, 0); err == nil {
			t.Fatal("limit 0 accepted")
		}
	})
}

// 数据库暂时不可用后从上次的位置继续，不结束订阅。
func TestDatabaseResumesAfterErrors(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db := dbtesting.Open(t, driver, broadcast.Migrations())
		h := broadcast.New(fastDatabase(db, time.Second), nil)
		defer h.Close()
		sub := h.Subscribe("room:1")
		broadcasttest.Ready(t, h)
		if _, err := db.ExecContext(ctx, "ALTER TABLE broadcasts RENAME TO broadcasts_away"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond) // 轮询失败几次
		if _, err := db.ExecContext(ctx, "ALTER TABLE broadcasts_away RENAME TO broadcasts"); err != nil {
			t.Fatal(err)
		}
		if err := h.Publish(ctx, "room:1", []byte("back")); err != nil {
			t.Fatal(err)
		}
		if got := broadcasttest.Receive(t, sub); string(got.Data) != "back" || sub.Err() != nil {
			t.Fatalf("got %q err=%v", got.Data, sub.Err())
		}
	})
}
