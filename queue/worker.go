package queue

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/logging"
)

var (
	// ErrLeaseLost 表示任务的租约已过期，任务可能已被其他实例领取。
	// 它也是处理函数 context 的取消原因：收到它时应停止处理。
	ErrLeaseLost = errors.New("queue: lease lost")
	// ErrShuttingDown 是处理函数 context 的取消原因：停止等待超时，任务已释放给其他实例。
	ErrShuttingDown = errors.New("queue: shutting down")
)

// dbTimeout 是领取之后确认、重试、续租和释放任务时单次操作的超时。这些操作不使用 Run 的 context，
// 停止过程中仍能写回处理结果。
const dbTimeout = 30 * time.Second

// Worker 从驱动领取已注册 topic 的到期任务，交给处理函数，并把结果写回驱动。多个实例竞争领取。
//
// 每个 topic 一个领取循环，同时处理的任务不超过它的并发数。处理函数返回 nil 时确认任务；
// 返回错误时按退避时间重试，次数用完或错误被 Permanent 标记时转入失败任务表。
// 处理期间每 Lease/3 续租一次，租约丢失时取消处理函数的 context，原因是 ErrLeaseLost。
//
// Run 的 ctx 取消后，先停止领取，再等待处理中的任务完成并写回结果，最多等待 Config.ShutdownTimeout；
// 超时仍未完成的任务立即释放给其他实例，处理函数的 context 以 ErrShuttingDown 取消。Run 在这一切完成后返回，
// 之后可以关闭数据库等连接。
//
//	w := queue.NewWorker(queue.Database(db), cfg, logger)
//	SendMail.Handle(w, mailer.Send)
//	err := w.Run(ctx)
type Worker struct {
	driver    Driver
	failed    *FailedJobs
	cfg       Config
	logger    *slog.Logger
	observers []Observer
	tracer    trace.Tracer // 为 nil 时不创建 span

	mu       sync.Mutex
	handlers []*handler
	started  bool
}

// handler 是一个 topic 的处理函数和它的设置。
type handler struct {
	topic       string
	fn          func(ctx context.Context, payload []byte) error
	concurrency int
	maxAttempts int
}

// WorkerOption 调整 NewWorker 创建的 Worker。
type WorkerOption func(*Worker)

// WithFailedJobs 指定失败任务写到哪里。数据库驱动默认写入自己的数据库，并且与删除任务在同一个事务中完成；
// 其他驱动必须指定，失败任务先写入 failed，再从驱动中确认删除。
func WithFailedJobs(failed *FailedJobs) WorkerOption {
	return func(w *Worker) { w.failed = failed }
}

// NewWorker 创建从驱动 d 领取任务的 Worker，cfg 是各 topic 的默认设置。logger 记录任务失败和驱动错误，
// nil 时使用 slog.Default()。d 为 nil、cfg 不合法，或者非数据库驱动没有使用 WithFailedJobs 时 panic。
func NewWorker(d Driver, cfg Config, logger *slog.Logger, opts ...WorkerOption) *Worker {
	if d == nil {
		panic("queue: nil driver")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "queue"))
	}
	w := &Worker{
		driver: d,
		cfg:    cfg,
		logger: logging.Or(logger).With(logging.AttrComponent, "queue"),
	}
	for _, opt := range opts {
		opt(w)
	}
	if db, ok := d.(*dbDriver); ok && w.failed == nil {
		w.failed = NewFailedJobs(db.db)
	}
	if w.failed == nil {
		panic(fmt.Sprintf("queue: driver %q needs WithFailedJobs", d.Name()))
	}
	return w
}

// handle 注册 topic 的处理函数，见 Topic.Handle。
func (w *Worker) handle(topic string, fn func(context.Context, []byte) error, opts []HandleOption) {
	if topic == "" {
		panic("queue: empty topic")
	}
	h := &handler{topic: topic, fn: fn, concurrency: w.cfg.Concurrency, maxAttempts: w.cfg.MaxAttempts}
	for _, opt := range opts {
		opt(h)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.started {
		panic(fmt.Sprintf("queue: Handle(%q) after Run", topic))
	}
	for _, other := range w.handlers {
		if other.topic == topic {
			panic(fmt.Sprintf("queue: topic %q handled twice", topic))
		}
	}
	w.handlers = append(w.handlers, h)
}

// Run 领取并处理已注册 topic 的任务，直到 ctx 被取消，然后按 Worker 的说明停止，返回 nil。
// 没有注册任何处理函数，或者第二次调用时返回错误。
func (w *Worker) Run(ctx context.Context) error {
	w.mu.Lock()
	handlers, started := w.handlers, w.started
	w.started = true
	w.mu.Unlock()
	switch {
	case started:
		return errors.New("queue: Run called twice")
	case len(handlers) == 0:
		return errors.New("queue: no handlers")
	}
	var wg sync.WaitGroup
	for _, h := range handlers {
		wg.Go(func() { w.consume(ctx, h) })
	}
	wg.Wait()
	return nil
}

// loop 是一个 topic 的领取循环正在处理的任务。
type loop struct {
	*handler
	slots    chan struct{}  // 空位数即还能领取的任务数
	inflight sync.WaitGroup // 已开始处理、还没写回结果的任务
	abandon  chan struct{}  // 停止等待超时，释放处理中的任务
}

// consume 领取 h 的任务直到 ctx 取消，然后等待处理中的任务写回结果。
func (w *Worker) consume(ctx context.Context, h *handler) {
	l := &loop{handler: h, slots: make(chan struct{}, h.concurrency), abandon: make(chan struct{})}
	w.claimLoop(ctx, l)
	w.drain(l)
}

// drain 等待处理中的任务写回结果，最多等待 ShutdownTimeout，超时后释放它们。
func (w *Worker) drain(l *loop) {
	idle := make(chan struct{})
	go func() {
		l.inflight.Wait()
		close(idle)
	}()
	t := time.NewTimer(w.cfg.ShutdownTimeout)
	defer t.Stop()
	select {
	case <-idle:
	case <-t.C:
		close(l.abandon)
		<-idle
	}
}

func (w *Worker) claimLoop(ctx context.Context, l *loop) {
	for {
		// 至少等到一个空位，再把其余空位一起用于这次领取。
		select {
		case l.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		n := 1
	fill:
		for n < l.concurrency {
			select {
			case l.slots <- struct{}{}:
				n++
			default:
				break fill
			}
		}

		claims, err := w.driver.Claim(ctx, ClaimRequest{Topic: l.topic, Max: n, Lease: w.cfg.Lease, PollInterval: w.cfg.PollInterval})
		for range n - len(claims) {
			<-l.slots
		}
		if err != nil && ctx.Err() == nil {
			w.logger.Error("claim jobs failed", "topic", l.topic, "error", err)
			wait(ctx, w.cfg.PollInterval)
		}
		if ctx.Err() != nil {
			// 停止时刚领取的任务不再开始，直接还回队列。
			for _, c := range claims {
				w.release(c)
				<-l.slots
			}
			return
		}
		for _, c := range claims {
			l.inflight.Add(1)
			go w.process(c, l)
		}
	}
}

func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// process 执行一个任务并写回结果，期间按 Lease/3 续租。
func (w *Worker) process(c Claim, l *loop) {
	defer l.inflight.Done()
	defer func() { <-l.slots }()

	j := c.Job()
	ctx, cancel := context.WithCancelCause(w.jobContext(j, l.maxAttempts))
	defer cancel(nil)
	w.observe(ctx, Event{Kind: JobStarted, Job: j})
	started := time.Now()
	result := make(chan error, 1)
	go func() { result <- call(ctx, l.fn, j.Payload) }()

	ticker := time.NewTicker(max(w.cfg.Lease/3, time.Millisecond))
	defer ticker.Stop()
	held := true
	for {
		select {
		case err := <-result:
			switch {
			case !held:
			case err == nil:
				w.ack(ctx, c, started)
			default:
				w.nack(ctx, c, err, l.maxAttempts, started)
			}
			return
		case <-ticker.C:
			if !held {
				continue
			}
			err := withTimeout(func(ctx context.Context) error { return c.Extend(ctx, w.cfg.Lease) })
			switch {
			case errors.Is(err, ErrLeaseLost):
				held = false
				w.logger.WarnContext(ctx, "job lease lost", "topic", j.Topic)
				w.observe(ctx, Event{Kind: JobLeaseLost, Job: j, Duration: time.Since(started)})
				cancel(ErrLeaseLost)
			case err != nil:
				w.logger.ErrorContext(ctx, "extend lease failed", "topic", j.Topic, "error", err)
			}
		case <-l.abandon:
			if held && w.release(c) {
				w.observe(ctx, Event{Kind: JobReleased, Job: j, Duration: time.Since(started)})
			}
			cancel(ErrShuttingDown)
			return
		}
	}
}

// jobContext 返回处理函数的 context：还原发布方的链路和 request_id，附上任务 ID、执行次数。
func (w *Worker) jobContext(j Job, maxAttempts int) context.Context {
	attrs := []slog.Attr{slog.String(logging.AttrMessageID, j.ID.String()), slog.Int(logging.AttrAttempt, j.Attempt)}
	for _, key := range propagated {
		if v := j.Metadata[key]; v != "" {
			attrs = append(attrs, slog.String(key, v))
		}
	}
	ctx := logging.WithAttrs(w.startSpan(context.Background(), j), attrs...)
	return context.WithValue(ctx, attemptKey{}, attemptInfo{j.Attempt, maxAttempts})
}

// call 调用处理函数，把 panic 转为错误。
func call(ctx context.Context, fn func(context.Context, []byte) error, payload []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Newf("queue: panic: %v\n%s", r, debug.Stack())
		}
	}()
	return fn(ctx, payload)
}

// withTimeout 以 dbTimeout 调用 fn，不受 Run 和处理函数的 context 影响。
func withTimeout(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	return fn(ctx)
}

func (w *Worker) ack(ctx context.Context, c Claim, started time.Time) {
	err := withTimeout(c.Ack)
	switch {
	case err == nil:
		w.observe(ctx, Event{Kind: JobSucceeded, Job: c.Job(), Duration: time.Since(started)})
	case errors.Is(err, ErrLeaseLost):
		w.logger.WarnContext(ctx, "job lease lost before acknowledgement", "topic", c.Job().Topic)
		w.observe(ctx, Event{Kind: JobLeaseLost, Job: c.Job(), Duration: time.Since(started)})
	default:
		w.logger.ErrorContext(ctx, "acknowledge finished job failed", "topic", c.Job().Topic, "error", err)
	}
}

func (w *Worker) nack(ctx context.Context, c Claim, cause error, maxAttempts int, started time.Time) {
	j := c.Job()
	if !IsPermanent(cause) && j.Attempt < maxAttempts {
		delay := retryDelay(cause, j.Attempt)
		at := time.Now().Add(delay)
		err := withTimeout(func(ctx context.Context) error { return c.Retry(ctx, at) })
		if err != nil {
			w.logger.ErrorContext(ctx, "schedule retry failed", "topic", j.Topic, "error", err)
			return
		}
		w.logger.WarnContext(ctx, "job failed, will retry", "topic", j.Topic, "retry_in", delay, "error", cause)
		w.observe(ctx, Event{Kind: JobRetrying, Job: j, Duration: time.Since(started), Err: cause, RetryAt: at})
		return
	}
	if err := withTimeout(func(ctx context.Context) error { return w.fail(ctx, c, cause) }); err != nil {
		w.logger.ErrorContext(ctx, "move job to failed jobs failed", "topic", j.Topic, "error", err)
		return
	}
	w.logger.ErrorContext(ctx, "job failed", "topic", j.Topic, "error", cause)
	w.observe(ctx, Event{Kind: JobFailed, Job: j, Duration: time.Since(started), Err: cause})
}

// failer 由能在一个事务中删除任务并写入失败记录的领取实现（数据库驱动）。
type failer interface {
	fail(ctx context.Context, record failedJob) error
}

// fail 把任务转入失败任务表。数据库驱动在一个事务中完成；其他驱动先写失败记录再确认任务，
// 两步之间进程崩溃时任务会被重新执行，再次失败时替换这条记录。
func (w *Worker) fail(ctx context.Context, c Claim, cause error) error {
	j := c.Job()
	record := failedJob{
		ID: j.ID, Driver: w.driver.Name(), Topic: j.Topic, Payload: j.Payload, Metadata: j.Metadata,
		Attempts: j.Attempt, Error: fmt.Sprintf("%+v", cause), FailedAt: time.Now().UTC(),
	}
	if f, ok := c.(failer); ok {
		return f.fail(ctx, record)
	}
	if err := w.failed.record(ctx, record); err != nil {
		return err
	}
	return c.Ack(ctx)
}

// release 把领取的任务还回队列，立即可被领取，本次领取不计入处理次数。返回是否还回成功。
func (w *Worker) release(c Claim) bool {
	err := withTimeout(c.Release)
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		w.logger.Error("release job failed", "topic", c.Job().Topic, logging.AttrMessageID, c.Job().ID.String(), "error", err)
	}
	return err == nil
}
