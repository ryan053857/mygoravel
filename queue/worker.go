package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/nsqio/go-nsq"
	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/logging"
)

// outputBufferTimeout 是 nsqd 缓冲发给消费者的消息的最长时间。
const outputBufferTimeout = 25 * time.Millisecond

// ErrShuttingDown 是处理函数 context 的取消原因：停止等待超时，任务已放回 NSQ。
var ErrShuttingDown = errors.New("queue: shutting down")

// dbTimeout 是写入失败任务、经由 outbox 安排重试时单次数据库操作的超时。这些操作不使用 Run 的 context，
// 停止过程中仍能写回处理结果。
const dbTimeout = 30 * time.Second

// Worker 转发已发布的任务，并处理已注册 topic 的任务。
//
// 转发：Topic.Publish 把任务写入 queue_outbox，Worker 把它们发布到 NSQ 后删除。发布任务的事务提交时
// PostgreSQL 通知 Worker 立即转发，另外每 Config.PollInterval 检查一次。执行时间离现在超过 Config.MaxDefer 的
// 任务留在 outbox 中，到时间再转发。多个 Worker 同时转发时互不重复。
//
// 处理：每个 topic 一个 NSQ 消费者，同时处理的任务不超过它的并发数，同一个应用的 Worker 使用同一个 channel，
// 每个任务只交给其中一个。处理函数返回 nil 时确认任务；返回错误时按退避时间重试，次数用完或错误被 Permanent
// 标记时转入失败任务表。处理期间每 Lease/3 延长一次租约。
//
// Run 的 ctx 取消后，先停止接收新任务和转发，再等待处理中的任务完成，最多等待 Config.ShutdownTimeout；
// 超时仍未完成的任务立即放回 NSQ，处理函数的 context 以 ErrShuttingDown 取消。Run 在这一切完成后返回，
// 之后可以关闭数据库。
//
//	w := queue.NewWorker(db, cfg, logger)
//	SendMail.Handle(w, mailer.Send)
//	err := w.Run(ctx)
type Worker struct {
	db        *bun.DB
	failed    *FailedJobs
	cfg       Config
	logger    *slog.Logger
	observers []Observer
	tracer    trace.Tracer // 为 nil 时不创建 span

	mu       sync.Mutex
	handlers []*handler
	started  bool

	// stopping 在停止时置位，之后收到的任务不再开始，直接放回。lifecycle 保护它和 inflight 的计数。
	lifecycle sync.RWMutex
	stopping  bool
	inflight  sync.WaitGroup
	abandon   chan struct{} // 停止等待超时，放回处理中的任务
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

// NewWorker 创建 Worker。db 是 queue_outbox 和 queue_failed_jobs 所在的数据库，必须已执行 Migrations 中的迁移；
// cfg 是 NSQ 的地址和各 topic 的默认设置；logger 记录任务失败和 NSQ 的错误，nil 时使用 slog.Default()。
// db 为 nil 或 cfg 不合法时 panic。
func NewWorker(db *bun.DB, cfg Config, logger *slog.Logger, opts ...WorkerOption) *Worker {
	if db == nil {
		panic("queue: nil database")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "queue"))
	}
	w := &Worker{
		db:      db,
		failed:  NewFailedJobs(db),
		cfg:     cfg,
		logger:  logging.Or(logger).With(logging.AttrComponent, "queue"),
		abandon: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(w)
	}
	return w
}

// handle 注册 topic 的处理函数，见 Topic.Handle。
func (w *Worker) handle(topic string, fn func(context.Context, []byte) error, opts []HandleOption) {
	if !validName(w.cfg.Prefix + topic) {
		panic(fmt.Sprintf("queue: topic %q must be letters, digits, '.', '_' or '-', at most 64 with the prefix", topic))
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

// Run 转发任务、处理已注册 topic 的任务，直到 ctx 被取消，然后按 Worker 的说明停止，返回 nil。
// 连不上 nsqd 时返回错误。没有注册处理函数时只转发。第二次调用时返回错误。
func (w *Worker) Run(ctx context.Context) error {
	w.mu.Lock()
	handlers, started := w.handlers, w.started
	w.started = true
	w.mu.Unlock()
	if started {
		return errors.New("queue: Run called twice")
	}

	producer, err := nsq.NewProducer(w.cfg.NSQD, nsq.NewConfig())
	if err != nil {
		return errors.Wrap(err, "queue: nsq producer")
	}
	producer.SetLogger(nsqLogger{w.logger}, nsq.LogLevelWarning)
	defer producer.Stop()
	if err := producer.Ping(); err != nil {
		return errors.Wrapf(err, "queue: connect to nsqd %s", w.cfg.NSQD)
	}

	var consumers []*nsq.Consumer
	defer func() {
		for _, c := range consumers {
			c.Stop()
			<-c.StopChan
		}
	}()
	for _, h := range handlers {
		c, err := w.consume(h)
		if err != nil {
			return err
		}
		consumers = append(consumers, c)
	}

	relayed := make(chan struct{})
	go func() {
		defer close(relayed)
		w.relay(ctx, producer)
	}()
	<-ctx.Done()

	w.lifecycle.Lock()
	w.stopping = true
	w.lifecycle.Unlock()
	for _, c := range consumers {
		c.Stop() // 发送 CLS：nsqd 不再投递新的任务
	}
	w.drain()
	<-relayed
	return nil
}

// consume 为 h 创建并连接 NSQ 消费者。
func (w *Worker) consume(h *handler) (*nsq.Consumer, error) {
	cfg := nsq.NewConfig()
	cfg.MaxInFlight = h.concurrency
	cfg.MsgTimeout = w.cfg.Lease
	cfg.MaxAttempts = 0 // 次数由 Worker 判断
	// nsqd 把发给消费者的消息攒在缓冲区中，默认最多 250ms 才发出，任务少时每个都要等这么久。
	// 25ms 是 nsqd 允许的最小值（--min-output-buffer-timeout）；-1 关闭缓冲会让 nsqd 偶尔停顿数十秒。
	cfg.OutputBufferTimeout = outputBufferTimeout
	c, err := nsq.NewConsumer(w.cfg.Prefix+h.topic, w.cfg.Channel, cfg)
	if err != nil {
		return nil, errors.Wrapf(err, "queue: nsq consumer for %q", h.topic)
	}
	c.SetLogger(nsqLogger{w.logger}, nsq.LogLevelWarning)
	c.AddConcurrentHandlers(nsq.HandlerFunc(func(m *nsq.Message) error {
		w.process(h, m)
		return nil
	}), h.concurrency)
	if len(w.cfg.Lookupd) > 0 {
		err = c.ConnectToNSQLookupds(w.cfg.Lookupd)
	} else {
		err = c.ConnectToNSQD(w.cfg.NSQD)
	}
	if err != nil {
		c.Stop()
		return nil, errors.Wrapf(err, "queue: consume %q", h.topic)
	}
	return c, nil
}

// drain 等待处理中的任务写回结果，最多等待 ShutdownTimeout，超时后放回它们。
func (w *Worker) drain() {
	idle := make(chan struct{})
	go func() {
		w.inflight.Wait()
		close(idle)
	}()
	t := time.NewTimer(w.cfg.ShutdownTimeout)
	defer t.Stop()
	select {
	case <-idle:
	case <-t.C:
		close(w.abandon)
		<-idle
	}
}

// begin 登记一个开始处理的任务；停止之后返回 false，任务不再开始。
func (w *Worker) begin() bool {
	w.lifecycle.RLock()
	defer w.lifecycle.RUnlock()
	if w.stopping {
		return false
	}
	w.inflight.Add(1)
	return true
}

// process 执行一个任务并写回结果，期间按 Lease/3 延长租约。
func (w *Worker) process(h *handler, m *nsq.Message) {
	m.DisableAutoResponse()
	if !w.begin() {
		m.RequeueWithoutBackoff(0) // 停止时刚收到的任务不再开始，直接放回
		return
	}
	defer w.inflight.Done()

	var env envelope
	if err := json.Unmarshal(m.Body, &env); err != nil {
		w.logger.Error("drop undecodable message", "topic", h.topic, "error", err)
		_ = withTimeout(func(ctx context.Context) error {
			return w.failed.record(ctx, failedJob{ID: uuid.Must(uuid.NewV7()), Topic: h.topic, Payload: m.Body,
				Attempts: int(m.Attempts), Error: fmt.Sprintf("%+v", errors.Wrap(err, "queue: decode message")), FailedAt: time.Now().UTC()})
		})
		m.Finish()
		return
	}
	j := Job{ID: env.ID, Topic: h.topic, Payload: env.Payload, Metadata: env.Metadata, Attempt: env.Attempts + int(m.Attempts)}
	if m.Attempts == 1 {
		j.AvailableAt = env.AvailableAt
	}
	ctx, cancel := context.WithCancelCause(w.jobContext(j, h.maxAttempts))
	defer cancel(nil)
	started := time.Now()
	w.observe(ctx, Event{Kind: JobStarted, Job: j, Started: started})
	result := make(chan error, 1)
	go func() { result <- call(ctx, h.fn, j.Payload) }()

	ticker := time.NewTicker(w.cfg.Lease / 3)
	defer ticker.Stop()
	for {
		select {
		case err := <-result:
			if err == nil {
				m.Finish()
				w.observe(ctx, Event{Kind: JobSucceeded, Job: j, Started: started, Duration: time.Since(started)})
				return
			}
			w.nack(ctx, m, j, env, err, h.maxAttempts, started)
			return
		case <-ticker.C:
			m.Touch()
		case <-w.abandon:
			m.RequeueWithoutBackoff(0)
			w.observe(ctx, Event{Kind: JobReleased, Job: j, Started: started, Duration: time.Since(started)})
			cancel(ErrShuttingDown)
			return
		}
	}
}

// nack 安排重试，或者把任务转入失败任务表。不超过 MaxDefer 的重试由 NSQ 延迟投递，更晚的写回 outbox。
func (w *Worker) nack(ctx context.Context, m *nsq.Message, j Job, env envelope, cause error, maxAttempts int, started time.Time) {
	if !IsPermanent(cause) && j.Attempt < maxAttempts {
		delay := retryDelay(cause, j.Attempt)
		at := time.Now().Add(delay)
		if delay > w.cfg.MaxDefer {
			later := env
			later.AvailableAt, later.Attempts = at.UTC(), j.Attempt
			if err := withTimeout(func(ctx context.Context) error { return w.postpone(ctx, j.Topic, later) }); err != nil {
				w.logger.ErrorContext(ctx, "schedule retry failed", "topic", j.Topic, "error", err)
				m.RequeueWithoutBackoff(w.cfg.MaxDefer)
				return
			}
			m.Finish()
		} else {
			m.RequeueWithoutBackoff(delay)
		}
		w.logger.WarnContext(ctx, "job failed, will retry", "topic", j.Topic, "retry_in", delay, "error", cause)
		w.observe(ctx, Event{Kind: JobRetrying, Job: j, Started: started, Duration: time.Since(started), Err: cause, RetryAt: at})
		return
	}
	record := failedJob{ID: j.ID, Topic: j.Topic, Payload: j.Payload, Metadata: j.Metadata,
		Attempts: j.Attempt, Error: fmt.Sprintf("%+v", cause), FailedAt: time.Now().UTC()}
	if err := withTimeout(func(ctx context.Context) error { return w.failed.record(ctx, record) }); err != nil {
		// 失败任务写不进去时不确认：稍后重新投递，再次失败时再写。
		w.logger.ErrorContext(ctx, "move job to failed jobs failed", "topic", j.Topic, "error", err)
		m.RequeueWithoutBackoff(min(w.cfg.MaxDefer, time.Minute))
		return
	}
	m.Finish()
	w.logger.ErrorContext(ctx, "job failed", "topic", j.Topic, "error", cause)
	w.observe(ctx, Event{Kind: JobFailed, Job: j, Started: started, Duration: time.Since(started), Err: cause})
}

// postpone 把 env 写回 outbox，到 env.AvailableAt 再转发。
func (w *Worker) postpone(ctx context.Context, topic string, env envelope) error {
	body, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return insertOutbox(ctx, w.db, []outboxRow{{Topic: topic, Body: body, AvailableAt: env.AvailableAt, CreatedAt: time.Now().UTC()}})
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

// wait 等待 d 或 ctx 结束。
func wait(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}
