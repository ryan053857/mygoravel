package queue_test

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
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
		queuetest.TestDriver(t, queuetest.Options{New: func(t *testing.T) (queue.Driver, *bun.DB) {
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

// jobs 是测试使用的 topic。
var jobs = queue.Topic[payload]("jobs")

// start 运行一个 Worker，register 在运行前注册处理函数；返回的函数停止 Worker 并等待它写回结果。
func start(t *testing.T, db *bun.DB, cfg queue.Config, register func(w *queue.Worker), opts ...queue.WorkerOption) func() {
	t.Helper()
	w := queue.NewWorker(queue.Database(db), cfg, slog.New(slog.DiscardHandler), opts...)
	register(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			if err := <-done; err != nil {
				t.Errorf("worker: %v", err)
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

func publish(t *testing.T, db bun.IDB, n int) {
	t.Helper()
	if err := jobs.Publish(context.Background(), queue.Database(db), payload{N: n}); err != nil {
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
		start(t, db, testConfig(), func(w *queue.Worker) {
			jobs.Handle(w, func(ctx context.Context, p payload) error {
				handled.Add(1)
				last.Store(int64(p.N))
				return nil
			})
		})

		rollback := errors.New("rollback")
		err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			publish(t, tx, 1)
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatal(err)
		}
		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			publish(t, tx, 2)
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
		start(t, db, testConfig(), func(w *queue.Worker) {
			jobs.Handle(w, func(context.Context, payload) error {
				got <- struct{}{}
				return nil
			})
		})
		publish(t, db, 1)
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
		cleanup := failed.Cleanup(24 * time.Hour)
		if _, err := cleanup(ctx, 0); err == nil {
			t.Fatal("expected error for non-positive limit")
		}
		if n, err := cleanup(ctx, 2); n != 2 || err != nil {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := cleanup(ctx, 10); n != 1 || err != nil {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		if rows := failedRows(t, db); len(rows) != 1 || rows[0].Attempts != 4 {
			t.Fatalf("remaining %+v", rows)
		}
	})
}

func TestFailedJobsQueries(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		ctx := context.Background()
		now := time.Now().UTC()
		ids := make([]uuid.UUID, 3)
		for i, topic := range []string{"mail", "mail", "report"} {
			ids[i] = uuid.New()
			_, err := db.ExecContext(ctx, `INSERT INTO queue_failed_jobs (id, driver, topic, payload, metadata, attempts, error, failed_at)
				VALUES (?, 'database', ?, ?, '{"request_id":"r1"}', 3, 'boom', ?)`, ids[i], topic, []byte("{}"), now.Add(time.Duration(i)*time.Minute))
			if err != nil {
				t.Fatal(err)
			}
		}
		failed := queue.NewFailedJobs(db)

		all, err := failed.List(ctx, queue.FailedQuery{})
		if err != nil || len(all) != 3 || all[0].ID != ids[2] || all[2].ID != ids[0] {
			t.Fatalf("list %+v err %v", all, err)
		}
		if j := all[0]; j.Topic != "report" || j.Attempts != 3 || j.Error != "boom" || j.Metadata["request_id"] != "r1" || j.Driver != "database" {
			t.Fatalf("job %+v", j)
		}
		mail, err := failed.List(ctx, queue.FailedQuery{Topic: "mail", Limit: 1, Offset: 1})
		if err != nil || len(mail) != 1 || mail[0].ID != ids[0] {
			t.Fatalf("mail page %+v err %v", mail, err)
		}
		stats, err := queue.Stats(ctx, queue.Database(db), failed)
		if err != nil || len(stats) != 2 || stats[0].Topic != "mail" || stats[0].Jobs[queue.StateFailed] != 2 ||
			stats[0].Jobs[queue.StateReady] != 0 || stats[1].Topic != "report" || stats[1].Jobs[queue.StateFailed] != 1 {
			t.Fatalf("stats %+v err %v", stats, err)
		}

		if j, err := failed.Get(ctx, ids[1]); err != nil || j.ID != ids[1] || j.Topic != "mail" {
			t.Fatalf("get %+v err %v", j, err)
		}
		if err := failed.Delete(ctx, ids[1]); err != nil {
			t.Fatal(err)
		}
		if _, err := failed.Get(ctx, ids[1]); !errors.Is(err, queue.ErrFailedJobNotFound) {
			t.Fatalf("get deleted: %v", err)
		}
		if err := failed.Delete(ctx, ids[1]); !errors.Is(err, queue.ErrFailedJobNotFound) {
			t.Fatalf("delete twice: %v", err)
		}
	})
}

func TestFailedJobsBulk(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		ctx := context.Background()
		now := time.Now().UTC()
		insert := func(driver, topic string, age time.Duration) uuid.UUID {
			id := uuid.New()
			_, err := db.ExecContext(ctx, `INSERT INTO queue_failed_jobs (id, driver, topic, payload, attempts, error, failed_at)
				VALUES (?, ?, ?, ?, 3, 'boom', ?)`, id, driver, topic, []byte("{}"), now.Add(-age))
			if err != nil {
				t.Fatal(err)
			}
			return id
		}
		for i := range 3 {
			insert("database", "mail", time.Duration(i+2)*time.Hour)
		}
		insert("redis", "mail", time.Hour)              // 驱动没有注册，跳过
		insert("database", "report", time.Hour)         // 其他 topic
		fresh := insert("database", "mail", -time.Hour) // before 之后失败
		failed := queue.NewFailedJobs(db)

		for _, bad := range []func() (int, error){
			func() (int, error) { return failed.RetryAll(ctx, "mail", now, 0) },
			func() (int, error) { return failed.RetryAll(ctx, "mail", time.Time{}, 10) },
			func() (int, error) { return failed.DeleteAll(ctx, "mail", now, 0) },
			func() (int, error) { return failed.DeleteAll(ctx, "mail", time.Time{}, 10) },
		} {
			if _, err := bad(); err == nil {
				t.Fatal("expected an argument error")
			}
		}

		if n, err := failed.RetryAll(ctx, "mail", now, 2); n != 2 || err != nil {
			t.Fatalf("first batch n=%d err=%v", n, err)
		}
		if n, err := failed.RetryAll(ctx, "mail", now, 2); n != 1 || err != nil {
			t.Fatalf("second batch n=%d err=%v", n, err)
		}
		if n, err := failed.RetryAll(ctx, "mail", now, 2); n != 0 || err != nil {
			t.Fatalf("third batch n=%d err=%v", n, err)
		}
		var queued []struct {
			Topic    string `bun:"topic"`
			Attempts int    `bun:"attempts"`
		}
		if err := db.NewSelect().Table("queue_jobs").Column("topic", "attempts").Scan(ctx, &queued); err != nil {
			t.Fatal(err)
		}
		if len(queued) != 3 || queued[0].Topic != "mail" || queued[0].Attempts != 0 {
			t.Fatalf("queued %+v", queued)
		}
		// 只统计数据库驱动转入的失败任务：redis 的那条不计入。
		stats, err := queue.Stats(ctx, queue.Database(db), failed)
		if err != nil || len(stats) != 2 || stats[0].Topic != "mail" || stats[0].Jobs[queue.StateFailed] != 1 ||
			stats[0].Jobs[queue.StateReady] != 3 || stats[1].Topic != "report" || stats[1].Jobs[queue.StateFailed] != 1 {
			t.Fatalf("stats %+v err %v", stats, err)
		}

		// 全部 topic：删除 before 之前的，before 之后失败的保留。
		if n, err := failed.DeleteAll(ctx, "", now, 10); n != 2 || err != nil {
			t.Fatalf("delete n=%d err=%v", n, err)
		}
		if rows := failedRows(t, db); len(rows) != 1 || rows[0].ID != fresh {
			t.Fatalf("remaining %+v", rows)
		}
	})
}

func TestWorkerRegistration(t *testing.T) {
	db := openDB(t, sqlite.Driver)
	newWorker := func() *queue.Worker { return queue.NewWorker(queue.Database(db), testConfig(), nil) }
	noop := func(context.Context, payload) error { return nil }
	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s: no panic", name)
			}
		}()
		fn()
	}

	if err := newWorker().Run(context.Background()); err == nil {
		t.Error("Run without handlers succeeded")
	}
	mustPanic("empty topic", func() { queue.Topic[payload]("").Handle(newWorker(), noop) })
	mustPanic("handled twice", func() {
		w := newWorker()
		jobs.Handle(w, noop)
		jobs.Handle(w, noop)
	})
	mustPanic("zero attempts", func() { queue.MaxAttempts(0) })
	mustPanic("zero concurrency", func() { queue.Concurrency(0) })
	mustPanic("nil observer", func() { queue.WithObserver(nil) })

	w := newWorker()
	jobs.Handle(w, noop)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if err := w.Run(ctx); err == nil {
		t.Error("second Run succeeded")
	}
	mustPanic("Handle after Run", func() { queue.Topic[payload]("other").Handle(w, noop) })
}

// Handle 的选项覆盖 Config：MaxAttempts 决定转入失败表之前的次数，也决定 AttemptFromContext 的 final；
// Concurrency 限制同时处理的任务数。
func TestHandleOptions(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		var finals atomic.Int64
		var running, peak, done atomic.Int64
		slow := queue.Topic[payload]("slow")
		start(t, db, testConfig(), func(w *queue.Worker) {
			jobs.Handle(w, func(ctx context.Context, p payload) error {
				if _, final := queue.AttemptFromContext(ctx); final {
					finals.Add(1)
				}
				return queue.RetryAfter(errors.New("boom"), 0)
			}, queue.MaxAttempts(2))
			slow.Handle(w, func(context.Context, payload) error {
				n := running.Add(1)
				for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
				}
				time.Sleep(50 * time.Millisecond)
				running.Add(-1)
				done.Add(1)
				return nil
			}, queue.Concurrency(2))
		})
		publish(t, db, 1)
		for i := range 6 {
			if err := slow.Publish(context.Background(), queue.Database(db), payload{N: i}); err != nil {
				t.Fatal(err)
			}
		}
		eventually(t, 5*time.Second, func() bool { return len(failedRows(t, db)) == 1 && done.Load() == 6 })
		if rows := failedRows(t, db); rows[0].Attempts != 2 || finals.Load() != 1 {
			t.Fatalf("failed %+v, final attempts seen %d", rows, finals.Load())
		}
		if p := peak.Load(); p != 2 {
			t.Fatalf("peak concurrency %d, want 2", p)
		}
	})
}
