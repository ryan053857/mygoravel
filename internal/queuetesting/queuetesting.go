// Package queuetesting 是队列驱动的一致性测试：数据库、Redis、NATS 驱动运行同一组场景，
// 保证 Subscriber 在任何驱动上的行为相同。
package queuetesting

import (
	"context"
	"encoding/json"
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

	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/queue"
)

// Backend 描述被测试的驱动。
type Backend struct {
	// New 为一个场景创建空的驱动，以及保存失败任务、已执行队列迁移的数据库。
	New func(t *testing.T) (queue.Driver, *bun.DB)
	// NoFencing 表示驱动无法发现过期的租约：旧领取的确认仍然生效。
	NoFencing bool
	// PartialStats 表示驱动只报告 Ready 和 Running，Running 可能包括等待重试的任务。
	PartialStats bool
}

// Run 运行全部场景。
func Run(t *testing.T, b Backend) {
	for _, sc := range []struct {
		name string
		run  func(t *testing.T, s *setup)
	}{
		{"publish and handle", publishAndHandle},
		{"delay", delay},
		{"retry with backoff", retryWithBackoff},
		{"failed jobs", failedJobs},
		{"permanent failures", permanentFailures},
		{"concurrent failures", concurrentFailures},
		{"competing subscribers", competingSubscribers},
		{"failing job does not block others", failingJobDoesNotBlock},
		{"lease is extended", leaseIsExtended},
		{"graceful shutdown", gracefulShutdown},
		{"shutdown timeout releases jobs", shutdownTimeoutReleases},
		{"expired lease is reclaimed", expiredLeaseIsReclaimed},
		{"observer events", observerEvents},
		{"stats", stats},
	} {
		t.Run(sc.name, func(t *testing.T) {
			d, db := b.New(t)
			sc.run(t, &setup{backend: b, driver: d, db: db, failed: queue.NewFailedJobs(db, d)})
		})
	}
}

type setup struct {
	backend Backend
	driver  queue.Driver
	db      *bun.DB
	failed  *queue.FailedJobs
}

type payload struct {
	N int `json:"n"`
}

func config() queue.Config {
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	cfg.Lease = 2 * time.Second
	cfg.ShutdownTimeout = 5 * time.Second
	return cfg
}

// start 运行一个 Router，register 在运行前注册处理函数；返回的函数停止 Router 并等待它退出。
func (s *setup) start(t *testing.T, cfg queue.Config, register func(r *message.Router, sub *queue.Subscriber), opts ...queue.SubscriberOption) func() {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	r := queue.NewRouter(logger)
	sub := queue.NewSubscriber(s.driver, cfg, logger, append([]queue.SubscriberOption{queue.WithFailedJobs(s.failed)}, opts...)...)
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

func (s *setup) publish(t *testing.T, topic string, n int, opts ...queue.PublishOption) {
	t.Helper()
	if err := queue.Publish(context.Background(), s.driver, topic, payload{N: n}, opts...); err != nil {
		t.Fatal(err)
	}
}

// claim 直接从驱动领取，最多等待 wait；用于检查队列中还剩什么。
func (s *setup) claim(t *testing.T, topic string, lease, wait time.Duration) []queue.Claim {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	claims, err := s.driver.Claim(ctx, queue.ClaimRequest{Topic: topic, Max: 10, Lease: lease, PollInterval: 20 * time.Millisecond})
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
	return claims
}

// empty 检查 topic 中没有可以领取的任务。
func (s *setup) empty(t *testing.T, topic string) {
	t.Helper()
	if claims := s.claim(t, topic, time.Second, 300*time.Millisecond); len(claims) != 0 {
		t.Fatalf("%d jobs left in %q", len(claims), topic)
	}
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

func (s *setup) failedRows(t *testing.T) []failedRow {
	t.Helper()
	var rows []failedRow
	if err := s.db.NewSelect().Model(&rows).Order("failed_at").Scan(context.Background()); err != nil {
		t.Fatal(err)
	}
	return rows
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

func attr(ctx context.Context, key string) string {
	for _, a := range logging.ContextAttrs(ctx) {
		if a.Key == key {
			return a.Value.String()
		}
	}
	return ""
}

func publishAndHandle(t *testing.T, s *setup) {
	type seen struct {
		n                         int
		requestID, msgID, attempt string
	}
	got := make(chan seen, 1)
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			got <- seen{p.N, attr(ctx, logging.AttrRequestID), attr(ctx, logging.AttrMessageID), attr(ctx, logging.AttrAttempt)}
			return nil
		})
	})
	ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "req-1"))
	if err := queue.Publish(ctx, s.driver, "jobs", payload{N: 7}); err != nil {
		t.Fatal(err)
	}
	select {
	case v := <-got:
		if v.n != 7 || v.requestID != "req-1" || v.attempt != "1" {
			t.Fatalf("seen %+v", v)
		}
		if _, err := uuid.Parse(v.msgID); err != nil {
			t.Fatalf("message id %q", v.msgID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("job not handled")
	}
	time.Sleep(100 * time.Millisecond)
	s.empty(t, "jobs")
}

func delay(t *testing.T, s *setup) {
	got := make(chan time.Time, 1)
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(context.Context, payload) error {
			got <- time.Now()
			return nil
		})
	})
	begin := time.Now()
	s.publish(t, "jobs", 1, queue.Delay(time.Second))
	select {
	case at := <-got:
		if d := at.Sub(begin); d < time.Second || d > 3*time.Second {
			t.Fatalf("handled after %v", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("delayed job not handled")
	}
}

func retryWithBackoff(t *testing.T, s *setup) {
	var mu sync.Mutex
	var attempts []string
	var done atomic.Bool
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
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
	s.publish(t, "jobs", 1)
	eventually(t, 5*time.Second, done.Load)
	if d := time.Since(begin); d < 900*time.Millisecond {
		t.Fatalf("retried after %v, want about 1s", d)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(attempts) != 2 || attempts[0] != "1" || attempts[1] != "2" {
		t.Fatalf("attempts %v", attempts)
	}
}

func failedJobs(t *testing.T, s *setup) {
	ctx := context.Background()
	cfg := config()
	cfg.MaxAttempts = 2
	var fail atomic.Bool
	fail.Store(true)
	var handled atomic.Int64
	s.start(t, cfg, func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			handled.Add(1)
			if fail.Load() {
				return errors.Newf("job %d failed", p.N)
			}
			return nil
		})
	})

	s.publish(t, "jobs", 1)
	eventually(t, 10*time.Second, func() bool { return len(s.failedRows(t)) == 1 })
	rows := s.failedRows(t)
	if r := rows[0]; r.Driver != s.driver.Name() || r.Topic != "jobs" || r.Attempts != 2 || !strings.Contains(r.Error, "job 1 failed") {
		t.Fatalf("failed row %+v", r)
	}
	if handled.Load() != 2 {
		t.Fatalf("handled %d times", handled.Load())
	}

	// 重新入队后从第 1 次开始处理。
	fail.Store(false)
	if err := s.failed.Retry(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return handled.Load() == 3 })
	if len(s.failedRows(t)) != 0 {
		t.Fatal("retried job still in failed jobs")
	}
	if err := s.failed.Retry(ctx, rows[0].ID); !errors.Is(err, queue.ErrFailedJobNotFound) {
		t.Fatalf("retry missing: %v", err)
	}
}

func permanentFailures(t *testing.T, s *setup) {
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			return queue.Permanent(errors.New("article deleted"))
		})
	})
	s.publish(t, "jobs", 1)
	// payload 不是合法 JSON：解码失败视为永久失败。
	bad := message.NewMessage(uuid.NewString(), []byte("not json"))
	if err := queue.NewPublisher(s.driver).Publish("jobs", bad); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, func() bool { return len(s.failedRows(t)) == 2 })
	for _, r := range s.failedRows(t) {
		if r.Attempts != 1 {
			t.Fatalf("permanent failure retried: %+v", r)
		}
		want := "article deleted"
		if r.ID.String() == bad.UUID {
			want = "decode"
		}
		if !strings.Contains(r.Error, want) {
			t.Fatalf("error %q, want %q", r.Error, want)
		}
	}
}

// 多个任务同时转入失败表时不能互相阻塞（MySQL 可重复读下的间隙锁会让它们死锁）。
func concurrentFailures(t *testing.T, s *setup) {
	const n = 20
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(context.Context, payload) error {
			return queue.Permanent(errors.New("gone"))
		})
	})
	for i := range n {
		s.publish(t, "jobs", i)
	}
	eventually(t, 10*time.Second, func() bool { return len(s.failedRows(t)) == n })
	for _, r := range s.failedRows(t) {
		if r.Attempts != 1 {
			t.Fatalf("job retried after a permanent failure: %+v", r)
		}
	}
}

func competingSubscribers(t *testing.T, s *setup) {
	const n = 40
	cfg := config()
	cfg.Concurrency = 5
	var mu sync.Mutex
	seen := map[int]int{}
	per := [2]int{}
	for i := range 2 {
		s.start(t, cfg, func(r *message.Router, sub *queue.Subscriber) {
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
	jobs := make([]queue.Job, n)
	for i := range jobs {
		jobs[i] = queue.Job{ID: uuid.Must(uuid.NewV7()), Topic: "jobs", Payload: fmt.Appendf(nil, `{"n":%d}`, i)}
	}
	if err := s.driver.Enqueue(context.Background(), jobs...); err != nil {
		t.Fatal(err)
	}
	begin := time.Now()
	eventually(t, 15*time.Second, func() bool { mu.Lock(); defer mu.Unlock(); return len(seen) == n })
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
}

func failingJobDoesNotBlock(t *testing.T, s *setup) {
	var ok atomic.Int64
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			if p.N == 0 {
				return errors.New("always fails")
			}
			ok.Add(1)
			return nil
		})
	})
	for i := range 11 {
		s.publish(t, "jobs", i)
	}
	eventually(t, 3*time.Second, func() bool { return ok.Load() == 10 })
}

func leaseIsExtended(t *testing.T, s *setup) {
	cfg := config()
	cfg.Lease = time.Second
	var handled atomic.Int64
	var cause atomic.Value
	for range 2 {
		s.start(t, cfg, func(r *message.Router, sub *queue.Subscriber) {
			queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
				handled.Add(1)
				time.Sleep(2500 * time.Millisecond)
				cause.Store(fmt.Sprint(context.Cause(ctx)))
				return nil
			})
		})
	}
	s.publish(t, "jobs", 1)
	eventually(t, 10*time.Second, func() bool { return cause.Load() != nil })
	time.Sleep(200 * time.Millisecond)
	if handled.Load() != 1 {
		t.Fatalf("handled %d times", handled.Load())
	}
	if c := cause.Load(); c != "<nil>" {
		t.Fatalf("context canceled: %v", c)
	}
}

func gracefulShutdown(t *testing.T, s *setup) {
	started := make(chan struct{})
	var finished atomic.Bool
	var cause atomic.Value
	stop := s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			close(started)
			time.Sleep(500 * time.Millisecond)
			cause.Store(fmt.Sprint(context.Cause(ctx)))
			finished.Store(true)
			return nil
		})
	})
	s.publish(t, "jobs", 1)
	<-started
	stop()
	if !finished.Load() {
		t.Fatal("router stopped before the handler finished")
	}
	if c := cause.Load(); c != "<nil>" {
		t.Fatalf("handler context canceled: %v", c)
	}
	s.empty(t, "jobs")
}

func shutdownTimeoutReleases(t *testing.T, s *setup) {
	cfg := config()
	cfg.ShutdownTimeout = 100 * time.Millisecond
	started := make(chan struct{})
	causes := make(chan error, 1)
	var rec recorder
	stop := s.start(t, cfg, func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			close(started)
			<-ctx.Done()
			causes <- context.Cause(ctx)
			return ctx.Err()
		})
	}, queue.WithObserver(rec.observe))
	s.publish(t, "jobs", 1)
	<-started
	stop()
	if c := <-causes; !errors.Is(c, queue.ErrShuttingDown) {
		t.Fatalf("cause %v", c)
	}
	if k := strings.Join(rec.kinds(t, 1), " "); k != "started released" {
		t.Fatalf("events %q", k)
	}
	// 释放的任务立即可以领取，被释放的那次执行不计入次数。
	claims := s.claim(t, "jobs", time.Second, time.Second)
	if len(claims) != 1 {
		t.Fatalf("released job not claimable: %d", len(claims))
	}
	if j := claims[0].Job(); j.Attempt != 1 && !s.backend.NoFencing {
		t.Fatalf("attempt after release = %d, want 1", j.Attempt)
	}
}

// 领取任务后进程崩溃：租约到期后任务被重新领取；能发现过期租约的驱动拒绝旧领取的确认。
func expiredLeaseIsReclaimed(t *testing.T, s *setup) {
	ctx := context.Background()
	const lease = 500 * time.Millisecond
	s.publish(t, "jobs", 1)
	claimedAt := time.Now()
	crashed := s.claim(t, "jobs", lease, time.Second)
	if len(crashed) != 1 || crashed[0].Job().Attempt != 1 {
		t.Fatalf("first claim %d", len(crashed))
	}
	// 租约期内不能再次领取。调用慢到超过租约时，租约已经正常过期，这次领取就是重新领取。
	reclaimed := s.claim(t, "jobs", lease, 100*time.Millisecond)
	if len(reclaimed) != 0 && time.Since(claimedAt) < lease {
		t.Fatal("claimed a leased job")
	}
	if len(reclaimed) == 0 {
		reclaimed = s.claim(t, "jobs", 10*time.Second, 3*time.Second)
	}
	if len(reclaimed) != 1 || reclaimed[0].Job().Attempt != 2 || reclaimed[0].Job().ID != crashed[0].Job().ID {
		t.Fatalf("reclaim %d", len(reclaimed))
	}

	if !s.backend.NoFencing {
		if err := crashed[0].Extend(ctx, time.Minute); !errors.Is(err, queue.ErrLeaseLost) {
			t.Fatalf("stale extend: %v", err)
		}
		if err := crashed[0].Ack(ctx); !errors.Is(err, queue.ErrLeaseLost) {
			t.Fatalf("stale ack: %v", err)
		}
		if err := crashed[0].Retry(ctx, time.Now()); !errors.Is(err, queue.ErrLeaseLost) {
			t.Fatalf("stale retry: %v", err)
		}
	}
	if err := reclaimed[0].Ack(ctx); err != nil {
		t.Fatal(err)
	}
	s.empty(t, "jobs")
}

// recorder 收集 Observer 报告的事件。
type recorder struct {
	mu     sync.Mutex
	events []queue.Event
}

func (r *recorder) observe(_ context.Context, e queue.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// kinds 返回第 n 个任务（payload.N）依次经历的事件。
func (r *recorder) kinds(t *testing.T, n int) []string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var kinds []string
	for _, e := range r.events {
		var p payload
		if err := json.Unmarshal(e.Job.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if p.N == n {
			kinds = append(kinds, e.Kind.String())
		}
	}
	return kinds
}

func observerEvents(t *testing.T, s *setup) {
	var rec recorder
	var first atomic.Bool
	s.start(t, config(), func(r *message.Router, sub *queue.Subscriber) {
		queue.Handle(r, sub, "jobs", func(ctx context.Context, p payload) error {
			switch {
			case p.N == 2 && !first.Swap(true):
				return errors.New("temporary")
			case p.N == 3:
				return queue.Permanent(errors.New("gone"))
			}
			return nil
		})
	}, queue.WithObserver(rec.observe), queue.WithObserver(func(context.Context, queue.Event) { panic("ignored") }))
	for n := 1; n <= 3; n++ {
		s.publish(t, "jobs", n)
	}
	want := map[int]string{
		1: "started succeeded",
		2: "started retrying started succeeded",
		3: "started failed",
	}
	eventually(t, 5*time.Second, func() bool {
		for n, w := range want {
			if strings.Join(rec.kinds(t, n), " ") != w {
				return false
			}
		}
		return true
	})
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, e := range rec.events {
		if e.Driver != s.driver.Name() || e.Job.Topic != "jobs" {
			t.Fatalf("event %+v", e)
		}
		switch e.Kind {
		case queue.JobRetrying:
			if e.Err == nil || e.RetryAt.IsZero() || e.Job.Attempt != 1 {
				t.Fatalf("retrying event %+v", e)
			}
		case queue.JobFailed:
			if !queue.IsPermanent(e.Err) || e.Duration <= 0 {
				t.Fatalf("failed event %+v", e)
			}
		case queue.JobSucceeded:
			if e.Duration <= 0 {
				t.Fatalf("succeeded event %+v", e)
			}
		}
	}
}

func stats(t *testing.T, s *setup) {
	ctx := context.Background()
	// running：领取后未确认；retrying：领取后安排一小时后重试。
	s.publish(t, "jobs", 1)
	if c := s.claim(t, "jobs", time.Hour, 5*time.Second); len(c) != 1 {
		t.Fatalf("claimed %d", len(c))
	}
	s.publish(t, "jobs", 2)
	retry := s.claim(t, "jobs", time.Hour, 5*time.Second)
	if len(retry) != 1 {
		t.Fatalf("claimed %d", len(retry))
	}
	if err := retry[0].Retry(ctx, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// ready 和 delayed。
	before := time.Now()
	s.publish(t, "jobs", 3)
	s.publish(t, "jobs", 4, queue.Delay(time.Hour))
	s.publish(t, "other", 5)

	var got []queue.TopicStats
	eventually(t, 5*time.Second, func() bool {
		var err error
		got, err = queue.Stats(ctx, s.driver)
		if err != nil {
			t.Fatal(err)
		}
		return len(got) == 2 && got[0].Ready == 1 && got[1].Ready == 1
	})
	jobs, other := got[0], got[1]
	if jobs.Topic != "jobs" || other.Topic != "other" {
		t.Fatalf("stats %+v", got)
	}
	if s.backend.PartialStats {
		if jobs.Running < 1 || jobs.Delayed != queue.Unknown || jobs.Retrying != queue.Unknown {
			t.Fatalf("stats %+v", jobs)
		}
		return
	}
	if jobs.Delayed != 1 || jobs.Retrying != 1 || jobs.Running != 1 {
		t.Fatalf("stats %+v", jobs)
	}
	if jobs.OldestReadyAt.Before(before.Add(-time.Second)) || jobs.OldestReadyAt.After(time.Now().Add(time.Second)) {
		t.Fatalf("oldest ready at %v, published at %v", jobs.OldestReadyAt, before)
	}
}
