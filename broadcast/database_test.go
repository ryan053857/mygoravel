package broadcast_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/database/dbtest"
)

func fastDatabase(db bun.IDB, gapTimeout time.Duration) *broadcast.Database {
	d := broadcast.NewDatabase(db, nil)
	broadcast.SetTimings(d, 10*time.Millisecond, gapTimeout, time.Hour)
	return d
}

func TestDatabase(t *testing.T) {
	broadcasttest.TestBackend(t, func(t *testing.T) (broadcast.Backend, broadcast.Backend) {
		db := dbtest.Open(t, broadcast.Migrations())
		return fastDatabase(db, time.Second), fastDatabase(db, time.Second)
	})
}

// 自增 id 先分配后提交：未提交的较小 id 在等待时间内提交时按顺序投递，回滚时超时后跳过。
func TestDatabaseWaitsForUncommittedIDs(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t, broadcast.Migrations())
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
}

func TestDatabaseCleanup(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t, broadcast.Migrations())
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
}

// 数据库暂时不可用后从上次的位置继续，不结束订阅。
func TestDatabaseResumesAfterErrors(t *testing.T) {
	ctx := context.Background()
	db := dbtest.Open(t, broadcast.Migrations())
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
}

// 本进程发布的消息不等轮询，立即投递。
func TestDatabaseDeliversLocalMessagesImmediately(t *testing.T) {
	db := dbtest.Open(t, broadcast.Migrations())
	d := broadcast.NewDatabase(db, nil)
	broadcast.SetTimings(d, time.Hour, time.Second, time.Hour) // 开始监听后不再轮询
	h := broadcast.New(d, nil)
	defer h.Close()
	broadcasttest.Ready(t, h)
	sub := h.Subscribe("room:1")
	if err := h.Publish(context.Background(), "room:1", []byte("now")); err != nil {
		t.Fatal(err)
	}
	select {
	case m := <-sub.C():
		if string(m.Data) != "now" {
			t.Fatalf("got %q", m.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("local message waited for polling")
	}
}

// 并发发布与轮询交错时，本进程和其他进程都恰好收到每条消息一次，同一个发布者的消息保持顺序。
func TestDatabaseDeliversEachMessageOnce(t *testing.T) {
	db := dbtest.Open(t, broadcast.Migrations())
	local := broadcast.New(fastDatabase(db, time.Second), nil)
	defer local.Close()
	remote := broadcast.New(fastDatabase(db, time.Second), nil)
	defer remote.Close()
	subs := map[string]*broadcast.Subscription{"local": local.Subscribe("room:1"), "remote": remote.Subscribe("room:1")}
	broadcasttest.Ready(t, local, remote)

	const publishers, each = 4, 15 // 一次轮询可能读到全部，总数不超过订阅的缓冲
	received := make(map[string][]string)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for name, sub := range subs {
		wg.Go(func() {
			for range publishers * each {
				m := broadcasttest.Receive(t, sub)
				mu.Lock()
				received[name] = append(received[name], string(m.Data))
				mu.Unlock()
			}
		})
	}
	var pubs sync.WaitGroup
	for p := range publishers {
		pubs.Go(func() {
			for i := range each {
				if err := local.Publish(context.Background(), "room:1", []byte(fmt.Sprintf("%d-%03d", p, i))); err != nil {
					t.Error(err)
				}
			}
		})
	}
	pubs.Wait()
	wg.Wait()

	for name, sub := range subs {
		select {
		case m := <-sub.C():
			t.Fatalf("%s received an extra %q", name, m.Data)
		case <-time.After(100 * time.Millisecond):
		}
		last := map[byte]string{}
		for _, data := range received[name] {
			if prev := last[data[0]]; data <= prev {
				t.Fatalf("%s: %q after %q", name, data, prev)
			}
			last[data[0]] = data
		}
	}
}
