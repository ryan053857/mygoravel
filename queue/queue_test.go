package queue_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
)

type payload struct {
	N int `json:"n"`
}

// jobs 是大多数测试使用的 topic。每个测试的 Worker 有自己的随机前缀，同名的 topic 互不影响。
var jobs = queue.Topic[payload]("jobs")

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	return dbtest.Open(t, queue.Migrations())
}

// start 运行一个 Worker，register 在运行前注册处理函数；返回的函数停止 Worker 并等待它返回。
func start(t *testing.T, db *bun.DB, cfg queue.Config, register func(w *queue.Worker), opts ...queue.WorkerOption) (stop func()) {
	t.Helper()
	w := queue.NewWorker(db, cfg, slog.New(slog.DiscardHandler), opts...)
	register(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("worker: %v", err)
				}
			case <-time.After(30 * time.Second):
				t.Error("worker did not stop")
			}
		})
	}
	t.Cleanup(stop)
	return stop
}

func publish(t *testing.T, db bun.IDB, n int, opts ...queue.PublishOption) {
	t.Helper()
	if err := jobs.Publish(context.Background(), db, payload{N: n}, opts...); err != nil {
		t.Fatal(err)
	}
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

func scheduledCount(t *testing.T, db bun.IDB) int {
	t.Helper()
	n, err := db.NewSelect().Table("queue_scheduled").Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

type failedRow struct {
	bun.BaseModel `bun:"table:queue_failed_jobs"`

	ID       uuid.UUID `bun:"id"`
	Topic    string    `bun:"topic"`
	Attempts int       `bun:"attempts"`
	Error    string    `bun:"error"`
}

func failedRows(t *testing.T, db bun.IDB) []failedRow {
	t.Helper()
	var rows []failedRow
	if err := db.NewSelect().Model(&rows).Order("failed_at").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rows
}

func attr(ctx context.Context, key string) string {
	for _, a := range logging.ContextAttrs(ctx) {
		if a.Key == key {
			return a.Value.String()
		}
	}
	return ""
}

func TestConfig(t *testing.T) {
	t.Parallel()
	if err := queue.DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*queue.Config){
		"no nsqd":         func(c *queue.Config) { c.NSQD = "" },
		"bad channel":     func(c *queue.Config) { c.Channel = "a b" },
		"bad prefix":      func(c *queue.Config) { c.Prefix = "blog:" },
		"zero lease":      func(c *queue.Config) { c.Lease = 0 },
		"zero max defer":  func(c *queue.Config) { c.MaxDefer = 0 },
		"zero attempts":   func(c *queue.Config) { c.MaxAttempts = 0 },
		"zero concurrent": func(c *queue.Config) { c.Concurrency = 0 },
	} {
		cfg := queue.DefaultConfig()
		change(&cfg)
		if cfg.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// 事务中发布的任务随事务提交，回滚时不留下；处理函数的 context 带着发布方的 request_id 和执行次数。
func TestPublishInTransaction(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	type seen struct {
		n                         int
		requestID, msgID, attempt string
	}
	got := make(chan seen, 2)
	stop := start(t, db, queuetest.Config(t), func(w *queue.Worker) {
		jobs.Handle(w, func(ctx context.Context, p payload) error {
			got <- seen{p.N, attr(ctx, logging.AttrRequestID), attr(ctx, logging.AttrMessageID), attr(ctx, logging.AttrAttempt)}
			return nil
		})
	})
	ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "req-1"))
	rollback := errors.New("rollback")
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := jobs.Publish(ctx, tx, payload{N: 1}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		return jobs.Publish(ctx, tx, payload{N: 2})
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v.n != 2 || v.requestID != "req-1" || v.attempt != "1" {
			t.Fatalf("seen %+v", v)
		}
		if _, err := uuid.Parse(v.msgID); err != nil {
			t.Fatalf("message id %q", v.msgID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job not handled")
	}
	select {
	case v := <-got:
		t.Fatalf("rolled back job handled: %+v", v)
	case <-time.After(300 * time.Millisecond):
	}
	// 转发之后确认了复制槽的位置：停止 Worker 后，槽中没有待转发的任务。
	stop()
	if p := queuetest.Pending(t, db, jobs); len(p) != 0 {
		t.Fatalf("pending after relay: %v", p)
	}
}

func TestPublishAll(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	var mu sync.Mutex
	var seen []int
	start(t, db, queuetest.Config(t), func(w *queue.Worker) {
		jobs.Handle(w, func(_ context.Context, p payload) error {
			mu.Lock()
			seen = append(seen, p.N)
			mu.Unlock()
			return nil
		})
	})
	payloads := make([]payload, 1200) // 超过一条 INSERT 的行数和一次转发的条数
	for i := range payloads {
		payloads[i] = payload{N: i}
	}
	if err := jobs.PublishAll(context.Background(), db, payloads); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == len(payloads)
	})
	mu.Lock()
	defer mu.Unlock()
	slices.Sort(seen)
	for i, n := range seen {
		if n != i {
			t.Fatalf("seen %d at %d", n, i)
		}
	}
	if err := jobs.PublishAll(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	if err := queue.Topic[int]("bad topic").Publish(context.Background(), db, 1); err == nil {
		t.Fatal("invalid topic accepted")
	}
}

// 不超过 MaxDefer 的延迟由 NSQ 完成；更晚的任务先存入 queue_scheduled，快到时间再写回 WAL。
func TestDelay(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	cfg := queuetest.Config(t)
	cfg.MaxDefer = time.Second
	got := make(chan int, 2)
	start(t, db, cfg, func(w *queue.Worker) {
		jobs.Handle(w, func(_ context.Context, p payload) error {
			got <- p.N
			return nil
		})
	})
	begin := time.Now()
	publish(t, db, 1, queue.Delay(500*time.Millisecond))
	publish(t, db, 2, queue.Delay(2*time.Second))
	eventually(t, time.Second, func() bool { return scheduledCount(t, db) == 1 }) // 只有 2 秒的任务
	// nsqd 每 5 秒刷新一次要扫描延迟消息的 channel 列表，测试新建的 channel 上的延迟消息最多晚约 5 秒。
	at := map[int]time.Duration{}
	for range 2 {
		select {
		case n := <-got:
			at[n] = time.Since(begin)
		case <-time.After(10 * time.Second):
			t.Fatalf("handled %v", at)
		}
	}
	if at[1] < 500*time.Millisecond || at[1] > 7*time.Second || at[2] < 2*time.Second || at[2] > 8*time.Second {
		t.Fatalf("handled after %v", at)
	}
}

// 失败的任务按 RetryAfter 重试：不超过 MaxDefer 时由 NSQ 延迟投递，更长的经由 queue_scheduled，执行次数接着计算。
func TestRetry(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	cfg := queuetest.Config(t)
	cfg.MaxDefer = time.Second
	type attempt struct {
		n  int
		at time.Time
	}
	got := make(chan attempt, 5)
	var events []queue.Event
	var mu sync.Mutex
	start(t, db, cfg, func(w *queue.Worker) {
		jobs.Handle(w, func(ctx context.Context, p payload) error {
			n, _ := queue.AttemptFromContext(ctx)
			got <- attempt{n: n, at: time.Now()}
			switch n {
			case 1:
				return queue.RetryAfter(errors.New("soon"), 200*time.Millisecond)
			case 2:
				return queue.RetryAfter(errors.New("later"), 2*time.Second)
			}
			return nil
		})
	}, queue.WithObserver(func(_ context.Context, e queue.Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}))
	publish(t, db, 1)
	var attempts []attempt
	for range 3 {
		select {
		case a := <-got:
			attempts = append(attempts, a)
		case <-time.After(15 * time.Second):
			t.Fatalf("only %d attempts", len(attempts))
		}
	}
	for i, a := range attempts {
		if a.n != i+1 {
			t.Fatalf("attempt %d reported as %d", i+1, a.n)
		}
	}
	// 上界宽松：测试新建的 channel 上的延迟消息最多晚约 5 秒，见 TestDelay。
	if d := attempts[1].at.Sub(attempts[0].at); d < 200*time.Millisecond || d > 7*time.Second {
		t.Fatalf("second attempt after %v", d)
	}
	if d := attempts[2].at.Sub(attempts[1].at); d < 2*time.Second || d > 9*time.Second {
		t.Fatalf("third attempt after %v", d)
	}
	eventually(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) == 6
	})
	mu.Lock()
	defer mu.Unlock()
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind.String())
	}
	want := []string{"started", "retrying", "started", "retrying", "started", "succeeded"}
	if !slices.Equal(kinds, want) {
		t.Fatalf("events %v", kinds)
	}
	// 首次执行和经由 queue_scheduled 的重试知道任务可以执行的时间，NSQ 重新投递的不知道。
	if events[0].Job.AvailableAt.IsZero() || !events[2].Job.AvailableAt.IsZero() || events[4].Job.AvailableAt.IsZero() {
		t.Fatalf("available at %v %v %v", events[0].Job.AvailableAt, events[2].Job.AvailableAt, events[4].Job.AvailableAt)
	}
	if events[0].Job.ID != events[4].Job.ID {
		t.Fatal("job id changed across retries")
	}
}

// 次数用完或 Permanent 的错误转入失败任务表；Retry 重新发布，执行次数从头计算。
func TestFailedJobs(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	cfg := queuetest.Config(t)
	cfg.MaxAttempts = 2
	var calls atomic.Int64
	var heal atomic.Bool
	permanent := queue.Topic[payload]("permanent")
	start(t, db, cfg, func(w *queue.Worker) {
		jobs.Handle(w, func(ctx context.Context, p payload) error {
			calls.Add(1)
			if heal.Load() {
				return nil
			}
			return queue.RetryAfter(errors.New("boom"), 0)
		})
		permanent.Handle(w, func(context.Context, payload) error {
			return queue.Permanent(errors.New("bad input"))
		})
	})
	publish(t, db, 1)
	if err := permanent.Publish(context.Background(), db, payload{N: 2}); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return len(failedRows(t, db)) == 2 })
	rows := failedRows(t, db)
	byTopic := map[string]failedRow{}
	for _, r := range rows {
		byTopic[r.Topic] = r
	}
	if r := byTopic["jobs"]; r.Attempts != 2 || calls.Load() != 2 {
		t.Fatalf("jobs failed %+v after %d calls", r, calls.Load())
	}
	if r := byTopic["permanent"]; r.Attempts != 1 {
		t.Fatalf("permanent failed %+v", r)
	}

	heal.Store(true)
	failed := queue.NewFailedJobs(db)
	if err := failed.Retry(context.Background(), byTopic["jobs"].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return calls.Load() == 3 })
	if err := failed.Retry(context.Background(), byTopic["jobs"].ID); !errors.Is(err, queue.ErrFailedJobNotFound) {
		t.Fatalf("retry twice: %v", err)
	}
	if rows := failedRows(t, db); len(rows) != 1 || rows[0].Topic != "permanent" {
		t.Fatalf("failed rows %+v", rows)
	}
}

// 同一个应用的多个 Worker 共同消费，每个任务只处理一次。
func TestCompetingWorkers(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	cfg := queuetest.Config(t)
	var mu sync.Mutex
	seen := map[int]int{}
	for range 2 {
		start(t, db, cfg, func(w *queue.Worker) {
			jobs.Handle(w, func(_ context.Context, p payload) error {
				mu.Lock()
				seen[p.N]++
				mu.Unlock()
				return nil
			})
		})
	}
	payloads := make([]payload, 200)
	for i := range payloads {
		payloads[i] = payload{N: i}
	}
	if err := jobs.PublishAll(context.Background(), db, payloads); err != nil {
		t.Fatal(err)
	}
	eventually(t, 10*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(seen) == len(payloads)
	})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	for n, c := range seen {
		if c != 1 {
			t.Fatalf("job %d handled %d times", n, c)
		}
	}
}

// 处理时间超过租约时自动延长，任务不会被重新投递。
func TestLeaseIsExtended(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	cfg := queuetest.Config(t)
	cfg.Lease = time.Second
	var calls atomic.Int64
	start(t, db, cfg, func(w *queue.Worker) {
		jobs.Handle(w, func(context.Context, payload) error {
			calls.Add(1)
			time.Sleep(3 * time.Second)
			return nil
		})
	})
	publish(t, db, 1)
	eventually(t, 5*time.Second, func() bool { return calls.Load() == 1 })
	time.Sleep(4 * time.Second)
	if n := calls.Load(); n != 1 {
		t.Fatalf("handled %d times", n)
	}
}

// 停止时等待处理中的任务完成；超过 ShutdownTimeout 的任务放回，处理函数的 context 以 ErrShuttingDown 取消，
// 下一个 Worker 重新处理它。
func TestShutdown(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	cfg := queuetest.Config(t)
	cfg.ShutdownTimeout = 300 * time.Millisecond
	slow := queue.Topic[payload]("slow")
	started := make(chan struct{}, 2)
	causes := make(chan error, 1)
	var finished atomic.Int64
	stop := start(t, db, cfg, func(w *queue.Worker) {
		jobs.Handle(w, func(context.Context, payload) error {
			started <- struct{}{}
			time.Sleep(100 * time.Millisecond)
			finished.Add(1)
			return nil
		})
		slow.Handle(w, func(ctx context.Context, p payload) error {
			started <- struct{}{}
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return ctx.Err()
		})
	})
	publish(t, db, 1)
	if err := slow.Publish(context.Background(), db, payload{N: 2}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatal("jobs not started")
		}
	}
	stop()
	if finished.Load() != 1 {
		t.Fatal("in-flight job did not finish before the worker stopped")
	}
	if cause := <-causes; !errors.Is(cause, queue.ErrShuttingDown) {
		t.Fatalf("cause %v", cause)
	}

	again := make(chan int, 1)
	start(t, db, cfg, func(w *queue.Worker) {
		slow.Handle(w, func(ctx context.Context, p payload) error {
			n, _ := queue.AttemptFromContext(ctx)
			again <- n
			return nil
		})
	})
	select {
	case n := <-again:
		if n != 2 {
			t.Fatalf("released job attempt %d", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("released job not handled again")
	}
}

// Handle 的选项覆盖 Config：MaxAttempts 决定转入失败表之前的次数，也决定 AttemptFromContext 的 final；
// Concurrency 限制同时处理的任务数。
func TestHandleOptions(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	var finals atomic.Int64
	var running, peak, done atomic.Int64
	slow := queue.Topic[payload]("slow")
	start(t, db, queuetest.Config(t), func(w *queue.Worker) {
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
	payloads := make([]payload, 6)
	if err := slow.PublishAll(context.Background(), db, payloads); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return len(failedRows(t, db)) == 1 && done.Load() == 6 })
	if rows := failedRows(t, db); rows[0].Attempts != 2 || finals.Load() != 1 {
		t.Fatalf("failed %+v, final attempts seen %d", rows, finals.Load())
	}
	if p := peak.Load(); p != 2 {
		t.Fatalf("peak concurrency %d, want 2", p)
	}
}

func TestWorkerRegistration(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	newWorker := func() *queue.Worker { return queue.NewWorker(db, queuetest.Config(t), nil) }
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
	mustPanic("nil database", func() { queue.NewWorker(nil, queue.DefaultConfig(), nil) })
	mustPanic("invalid topic", func() { queue.Topic[payload]("a b").Handle(newWorker(), noop) })
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

	cfg := queuetest.Config(t)
	cfg.NSQD = "127.0.0.1:1"
	if err := queue.NewWorker(db, cfg, nil).Run(context.Background()); err == nil {
		t.Error("Run without a reachable nsqd succeeded")
	}
}

func TestPermanent(t *testing.T) {
	t.Parallel()
	base := errors.New("bad input")
	err := queue.Permanent(base)
	if !queue.IsPermanent(err) || !errors.Is(err, base) || queue.Permanent(nil) != nil {
		t.Fatalf("permanent %v", err)
	}
	if queue.IsPermanent(base) {
		t.Fatal("plain error is permanent")
	}
}

func TestAttemptFromContextOutsideJob(t *testing.T) {
	t.Parallel()
	if n, final := queue.AttemptFromContext(context.Background()); n != 0 || final {
		t.Fatalf("attempt %d final %v", n, final)
	}
}
