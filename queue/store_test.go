package queue_test

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
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
	"github.com/ryan053857/mygoravel/logging"
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
	sub := queue.NewSubscriber(db, cfg, logger)
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
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("router: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Error("router did not stop")
			}
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

func count(t *testing.T, db bun.IDB, table string) int {
	t.Helper()
	n, err := db.NewSelect().Table(table).Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func publish(t *testing.T, db bun.IDB, topic string, n int, opts ...queue.PublishOption) {
	t.Helper()
	if err := queue.Publish(context.Background(), queue.NewPublisher(db), topic, payload{N: n}, opts...); err != nil {
		t.Fatal(err)
	}
}

func attr(ctx context.Context, key string) string {
	for _, a := range logging.ContextAttrs(ctx) {
		if a.Key == key {
			return a.Value.String()
		}
	}
	return ""
}

func TestPublishAndHandle(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		type seen struct {
			n                         int
			requestID, msgID, attempt string
		}
		got := make(chan seen, 1)
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				got <- seen{p.N, attr(ctx, logging.AttrRequestID), attr(ctx, logging.AttrMessageID), attr(ctx, logging.AttrAttempt)}
				return nil
			})
		})

		ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "req-1"))
		if err := queue.Publish(ctx, queue.NewPublisher(db), "jobs", payload{N: 7}); err != nil {
			t.Fatal(err)
		}
		select {
		case s := <-got:
			if s.n != 7 || s.requestID != "req-1" || s.attempt != "1" {
				t.Fatalf("seen %+v", s)
			}
			if _, err := uuid.Parse(s.msgID); err != nil {
				t.Fatalf("message id %q", s.msgID)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("job not handled")
		}
		eventually(t, 5*time.Second, func() bool { return count(t, db, "queue_jobs") == 0 })
	})
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

func TestDelay(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		got := make(chan time.Time, 1)
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(context.Context, payload) error {
				got <- time.Now()
				return nil
			})
		})
		begin := time.Now()
		publish(t, db, "jobs", 1, queue.Delay(time.Second))
		select {
		case at := <-got:
			if d := at.Sub(begin); d < time.Second || d > 2500*time.Millisecond {
				t.Fatalf("handled after %v", d)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("delayed job not handled")
		}
	})
}

func TestRetryWithBackoff(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		var attempts []string
		var mu sync.Mutex
		var done atomic.Bool
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				mu.Lock()
				attempts = append(attempts, attr(ctx, logging.AttrAttempt))
				first := len(attempts) == 1
				mu.Unlock()
				if first {
					return errors.New("temporary")
				}
				done.Store(true)
				return nil
			})
		})
		begin := time.Now()
		publish(t, db, "jobs", 1)
		eventually(t, 5*time.Second, done.Load)
		if d := time.Since(begin); d < 900*time.Millisecond {
			t.Fatalf("retried after %v, want about 1s", d)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(attempts) != 2 || attempts[0] != "1" || attempts[1] != "2" {
			t.Fatalf("attempts %v", attempts)
		}
		eventually(t, 5*time.Second, func() bool { return count(t, db, "queue_jobs") == 0 })
	})
}

type failedRow struct {
	bun.BaseModel `bun:"table:queue_failed_jobs"`

	ID       uuid.UUID `bun:"id"`
	Driver   string    `bun:"driver"`
	Topic    string    `bun:"topic"`
	Attempts int       `bun:"attempts"`
	Error    string    `bun:"error"`
	FailedAt time.Time `bun:"failed_at"`
}

func failedRows(t *testing.T, db bun.IDB) []failedRow {
	t.Helper()
	var rows []failedRow
	if err := db.NewSelect().Model(&rows).Order("failed_at").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestFailedJobs(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		ctx := context.Background()
		cfg := testConfig()
		cfg.MaxAttempts = 2
		var fail atomic.Bool
		fail.Store(true)
		var handled atomic.Int64
		start(t, db, cfg, func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				handled.Add(1)
				if fail.Load() {
					return errors.Newf("job %d failed", p.N)
				}
				return nil
			})
		})

		publish(t, db, "jobs", 1)
		eventually(t, 10*time.Second, func() bool { return len(failedRows(t, db)) == 1 })
		rows := failedRows(t, db)
		if r := rows[0]; r.Driver != "database" || r.Topic != "jobs" || r.Attempts != 2 || !strings.Contains(r.Error, "job 1 failed") {
			t.Fatalf("failed row %+v", r)
		}
		if handled.Load() != 2 || count(t, db, "queue_jobs") != 0 {
			t.Fatalf("handled %d, jobs %d", handled.Load(), count(t, db, "queue_jobs"))
		}

		// 重新入队后从第 1 次开始处理。
		fail.Store(false)
		failed := queue.NewFailedJobs(db)
		if err := failed.Retry(ctx, rows[0].ID); err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, func() bool { return handled.Load() == 3 })
		eventually(t, 5*time.Second, func() bool { return count(t, db, "queue_jobs") == 0 })
		if len(failedRows(t, db)) != 0 {
			t.Fatal("retried job still in failed jobs")
		}
		if err := failed.Retry(ctx, rows[0].ID); !errors.Is(err, queue.ErrFailedJobNotFound) {
			t.Fatalf("retry missing: %v", err)
		}
	})
}

func TestPermanentFailures(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				return queue.Permanent(errors.New("article deleted"))
			})
		})
		publish(t, db, "jobs", 1)
		// payload 不是合法 JSON：解码失败视为永久失败。
		bad := message.NewMessage(uuid.NewString(), []byte("not json"))
		if err := queue.NewPublisher(db).Publish("jobs", bad); err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, func() bool { return len(failedRows(t, db)) == 2 })
		for _, r := range failedRows(t, db) {
			if r.Attempts != 1 {
				t.Fatalf("permanent failure retried: %+v", r)
			}
			if r.ID.String() == bad.UUID && !strings.Contains(r.Error, "decode") || r.ID.String() != bad.UUID && !strings.Contains(r.Error, "article deleted") {
				t.Fatalf("error %q", r.Error)
			}
		}
	})
}

func TestCompetingSubscribers(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		const n = 40
		cfg := testConfig()
		cfg.Concurrency = 5
		var mu sync.Mutex
		seen := map[int]int{}
		per := [2]int{}
		for i := range 2 {
			start(t, db, cfg, func(r *message.Router, sub *queue.Subscriber) {
				queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
					time.Sleep(100 * time.Millisecond)
					mu.Lock()
					seen[p.N]++
					per[i]++
					mu.Unlock()
					return nil
				})
			})
		}
		err := db.RunInTx(context.Background(), nil, func(ctx context.Context, tx bun.Tx) error {
			for i := range n {
				publish(t, tx, "jobs", i)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		begin := time.Now()
		eventually(t, 10*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == n })
		elapsed := time.Since(begin)
		time.Sleep(200 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		for k, v := range seen {
			if v != 1 {
				t.Fatalf("job %d handled %d times", k, v)
			}
		}
		// 串行需要 4 秒；两个实例各 5 个并发约 0.4 秒。
		if elapsed > 2*time.Second || per[0] == 0 || per[1] == 0 {
			t.Fatalf("elapsed %v, per subscriber %v", elapsed, per)
		}
	})
}

func TestFailingJobDoesNotBlockOthers(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		var ok atomic.Int64
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				if p.N == 0 {
					return errors.New("always fails")
				}
				ok.Add(1)
				return nil
			})
		})
		for i := range 11 {
			publish(t, db, "jobs", i)
		}
		eventually(t, 3*time.Second, func() bool { return ok.Load() == 10 })
	})
}

func TestLeaseIsExtended(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		cfg := testConfig()
		cfg.Lease = time.Second
		var handled atomic.Int64
		var cause atomic.Value
		for range 2 {
			start(t, db, cfg, func(r *message.Router, sub *queue.Subscriber) {
				queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
					handled.Add(1)
					time.Sleep(2500 * time.Millisecond)
					cause.Store(fmt.Sprint(context.Cause(ctx)))
					return nil
				})
			})
		}
		publish(t, db, "jobs", 1)
		eventually(t, 5*time.Second, func() bool { return count(t, db, "queue_jobs") == 0 })
		if handled.Load() != 1 {
			t.Fatalf("handled %d times", handled.Load())
		}
		if c := cause.Load(); c != "<nil>" {
			t.Fatalf("context canceled: %v", c)
		}
	})
}

func TestGracefulShutdown(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		started := make(chan struct{})
		var finished atomic.Bool
		var cause atomic.Value
		stop := start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				close(started)
				time.Sleep(500 * time.Millisecond)
				cause.Store(fmt.Sprint(context.Cause(ctx)))
				finished.Store(true)
				return nil
			})
		})
		publish(t, db, "jobs", 1)
		<-started
		stop()
		if !finished.Load() {
			t.Fatal("router stopped before the handler finished")
		}
		if c := cause.Load(); c != "<nil>" {
			t.Fatalf("handler context canceled: %v", c)
		}
		if n := count(t, db, "queue_jobs"); n != 0 {
			t.Fatalf("finished job not deleted: %d left", n)
		}
	})
}

func TestShutdownTimeoutReleasesJobs(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		cfg := testConfig()
		cfg.ShutdownTimeout = 100 * time.Millisecond
		started := make(chan struct{})
		causes := make(chan error, 1)
		stop := start(t, db, cfg, func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				close(started)
				<-ctx.Done()
				causes <- context.Cause(ctx)
				return ctx.Err()
			})
		})
		publish(t, db, "jobs", 1)
		<-started
		stop()
		if c := <-causes; !errors.Is(c, queue.ErrShuttingDown) {
			t.Fatalf("cause %v", c)
		}
		var row struct {
			bun.BaseModel `bun:"table:queue_jobs"`
			Attempts      int          `bun:"attempts"`
			AvailableAt   time.Time    `bun:"available_at"`
			ReservedAt    bun.NullTime `bun:"reserved_at"`
		}
		if err := db.NewSelect().Model(&row).Scan(context.Background()); err != nil {
			t.Fatal(err)
		}
		if row.Attempts != 0 || row.AvailableAt.After(time.Now()) || !row.ReservedAt.IsZero() {
			t.Fatalf("released job %+v", row)
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

// 多个任务同时转入失败表时不能互相阻塞（MySQL 可重复读下的间隙锁会让它们死锁）。
func TestConcurrentFailures(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := openDB(t, driver)
		const n = 20
		start(t, db, testConfig(), func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(context.Context, payload) error {
				return queue.Permanent(errors.New("gone"))
			})
		})
		err := db.RunInTx(context.Background(), nil, func(ctx context.Context, tx bun.Tx) error {
			for i := range n {
				publish(t, tx, "jobs", i)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		eventually(t, 5*time.Second, func() bool { return len(failedRows(t, db)) == n })
		for _, r := range failedRows(t, db) {
			if r.Attempts != 1 {
				t.Fatalf("job retried after a permanent failure: %+v", r)
			}
		}
	})
}
