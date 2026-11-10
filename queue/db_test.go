package queue_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/internal/queuetesting"
	"github.com/ryan053857/mygoravel/queue"
)

type payload struct {
	N int `json:"n"`
}

// openDB 打开一个已执行队列迁移的测试数据库。
func openDB(t *testing.T, driver database.Driver) *bun.DB {
	t.Helper()
	return dbtesting.Open(t, driver, queue.Migrations())
}

// 数据库驱动在每种数据库上运行驱动一致性测试。
func TestDatabaseDriver(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		queuetesting.Run(t, queuetesting.Backend{New: func(t *testing.T) (queue.Driver, *bun.DB) {
			db := openDB(t, driver)
			return queue.Database(db), db
		}})
	})
}

func testConfig() queue.Config {
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	cfg.Lease = 2 * time.Second
	cfg.ShutdownTimeout = 5 * time.Second
	return cfg
}

// start 运行一个 Router，register 在运行前注册处理函数；返回的函数停止 Router 并等待它退出。
func start(t *testing.T, db *bun.DB, cfg queue.Config, register func(r *message.Router, sub *queue.Subscriber)) func() {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	r := queue.NewRouter(logger)
	sub := queue.NewSubscriber(queue.Database(db), cfg, logger)
	register(r, sub)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	select {
	case <-r.Running():
	case err := <-done:
		t.Fatalf("router exited: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("router did not start")
	}
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			<-done
			if err := sub.Close(); err != nil {
				t.Errorf("close subscriber: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func publish(t *testing.T, db bun.IDB, topic string, n int) {
	t.Helper()
	if err := queue.Publish(context.Background(), queue.Database(db), topic, payload{N: n}); err != nil {
		t.Fatal(err)
	}
}

type failedRow struct {
	bun.BaseModel `bun:"table:queue_failed_jobs"`

	ID       uuid.UUID `bun:"id"`
	Attempts int       `bun:"attempts"`
}

func failedRows(t *testing.T, db bun.IDB) []failedRow {
	t.Helper()
	var rows []failedRow
	if err := db.NewSelect().Model(&rows).Order("failed_at").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPublishInTransaction(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		ctx := context.Background()
		var handled atomic.Int64
		var last atomic.Int64
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				handled.Add(1)
				last.Store(int64(p.N))
				return nil
			})
		})

		rollback := errors.New("rollback")
		err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			publish(t, tx, "jobs", 1)
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			publish(t, tx, "jobs", 2)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, func() bool { return handled.Load() == 1 })
		time.Sleep(200 * time.Millisecond)
		if handled.Load() != 1 || last.Load() != 2 {
			t.Fatalf("handled %d, last %d", handled.Load(), last.Load())
		}
	})
}

func TestOpenTransactionDoesNotStallQueue(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		if driver == sqlite.Driver {
			t.Skip("SQLite 同一时刻只有一个写者，未提交的写事务会让领取等待")
		}
		db := openDB(t, driver)
		ctx := context.Background()
		if _, err := db.ExecContext(ctx, "CREATE TABLE unrelated (id int)"); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if _, err := tx.ExecContext(ctx, "INSERT INTO unrelated VALUES (1)"); err != nil {
			t.Fatal(err)
		}

		got := make(chan struct{}, 1)
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(context.Context, payload) error {
				got <- struct{}{}
				return nil
			})
		})
		publish(t, db, "jobs", 1)
		select {
		case <-got:
		case <-time.After(2 * time.Second):
			t.Fatal("job stalled by an unrelated open transaction")
		}
	})
}

func TestCleanupFailedJobs(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		ctx := context.Background()
		now := time.Now().UTC()
		for i, age := range []time.Duration{72 * time.Hour, 48 * time.Hour, 30 * time.Hour, time.Hour} {
			_, err := db.ExecContext(ctx, `INSERT INTO queue_failed_jobs (id, driver, topic, payload, attempts, error, failed_at)
				VALUES (?, 'database', 'jobs', '', ?, 'boom', ?)`, uuid.New(), i+1, now.Add(-age))
			if err != nil {
				t.Fatal(err)
			}
		}
		failed := queue.NewFailedJobs(db)
		if _, err := failed.Cleanup(ctx, now, 0); err == nil {
			t.Fatal("expected error for non-positive limit")
		}
		before := now.Add(-24 * time.Hour)
		if n, err := failed.Cleanup(ctx, before, 2); n != 2 || err != nil {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := failed.Cleanup(ctx, before, 10); n != 1 || err != nil {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		if rows := failedRows(t, db); len(rows) != 1 || rows[0].Attempts != 4 {
			t.Fatalf("remaining %+v", rows)
		}
	})
}
