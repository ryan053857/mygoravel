package queue

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging"
)

var (
	// ErrLeaseLost 表示任务的租约已过期，任务可能已被其他实例领取。
	// 它也是处理函数 context 的取消原因：收到它时应停止处理。
	ErrLeaseLost = errors.New("queue: lease lost")
	// ErrShuttingDown 是处理函数 context 的取消原因：停止等待超时，任务已释放给其他实例。
	ErrShuttingDown = errors.New("queue: shutting down")
)

// dbTimeout 是领取、确认和释放任务时单次操作的超时。这些操作不使用 Router 的 context，
// 停止过程中仍能写回处理结果。
const dbTimeout = 30 * time.Second

// Subscriber 从驱动领取到期的任务，实现 message.Subscriber。
//
// 每次 Subscribe 为一个 topic 启动一个领取循环，同时处理的任务不超过 Config.Concurrency。
// 处理函数 ack 后确认任务；nack 后按退避时间重试，次数用完或错误被 Permanent 标记时转入失败任务表。
// 处理期间每 Lease/3 续租一次，租约丢失时取消处理函数的 context，原因是 ErrLeaseLost。
//
// Subscribe 的 ctx 取消（Router 停止）或调用 Close 后，先停止领取，再等待处理中的任务完成，
// 最多等待 Config.ShutdownTimeout；超时仍未完成的任务立即释放给其他实例，处理函数的 context 以 ErrShuttingDown 取消。
// Router 停止时取消的 context 不会传给处理函数。
//
// Router 的 Run 在处理函数返回后就会返回，此时处理结果可能还没有写回。
// 在 Run 返回之后、关闭数据库等连接之前调用 Close，等待写回完成：
//
//	sub := queue.NewSubscriber(queue.Database(db), cfg, logger)
//	defer sub.Close()
type Subscriber struct {
	driver    Driver
	failed    *FailedJobs
	cfg       Config
	logger    *slog.Logger
	observers []Observer

	mu     sync.Mutex
	closed bool
	loops  sync.WaitGroup // 每个 topic 的领取循环，包括停止时等待处理中的任务

	closing   chan struct{} // 停止领取
	done      chan struct{}
	closeOnce sync.Once
}

var _ message.Subscriber = (*Subscriber)(nil)

// SubscriberOption 调整 NewSubscriber 创建的订阅者。
type SubscriberOption func(*Subscriber)

// WithFailedJobs 指定失败任务写到哪里。数据库驱动默认写入自己的数据库，并且与删除任务在同一个事务中完成；
// 其他驱动必须指定，失败任务先写入 failed，再从驱动中确认删除。
func WithFailedJobs(failed *FailedJobs) SubscriberOption {
	return func(s *Subscriber) { s.failed = failed }
}

// NewSubscriber 创建从驱动 d 领取任务的订阅者。logger 记录任务失败和驱动错误，nil 时使用 slog.Default()。
// cfg 不合法，或者非数据库驱动没有使用 WithFailedJobs 时 panic。
func NewSubscriber(d Driver, cfg Config, logger *slog.Logger, opts ...SubscriberOption) *Subscriber {
	if d == nil {
		panic("queue: nil driver")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "queue"))
	}
	s := &Subscriber{
		driver:  d,
		cfg:     cfg,
		logger:  logging.Or(logger).With(logging.AttrComponent, "queue"),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	if db, ok := d.(*dbDriver); ok && s.failed == nil {
		s.failed = NewFailedJobs(db.db)
	}
	if s.failed == nil {
		panic(fmt.Sprintf("queue: driver %q needs WithFailedJobs", d.Name()))
	}
	return s
}

// Subscribe 实现 message.Subscriber：开始领取 topic 的任务。ctx 取消或 Close 后停止领取并关闭返回的通道。
func (s *Subscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	if topic == "" {
		return nil, errors.New("queue: empty topic")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("queue: subscriber closed")
	}
	out := make(chan *message.Message)
	s.loops.Add(1)
	go s.consume(ctx, topic, out)
	return out, nil
}

// Close 实现 message.Subscriber，见 Subscriber 的说明。可以重复调用，每次都在停止完成后返回。
func (s *Subscriber) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.closing)
		s.loops.Wait()
		close(s.done)
	})
	<-s.done
	return nil
}

// loop 是一个 topic 的领取循环正在处理的任务。
type loop struct {
	slots    chan struct{}  // 空位数即还能领取的任务数
	inflight sync.WaitGroup // 已交给 Router、还没写回结果的任务
	abandon  chan struct{}  // 停止等待超时，释放处理中的任务
}

func (s *Subscriber) consume(ctx context.Context, topic string, out chan<- *message.Message) {
	l := &loop{slots: make(chan struct{}, s.cfg.Concurrency), abandon: make(chan struct{})}
	defer s.loops.Done()
	defer s.drain(l)
	defer close(out)

	// 领取在 ctx 取消或 Close 时停止。
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.closing:
			cancel()
		case <-ctx.Done():
		}
	}()
	s.claimLoop(ctx, topic, out, l)
}

// drain 等待处理中的任务写回结果，最多等待 ShutdownTimeout，超时后释放它们。
func (s *Subscriber) drain(l *loop) {
	idle := make(chan struct{})
	go func() {
		l.inflight.Wait()
		close(idle)
	}()
	t := time.NewTimer(s.cfg.ShutdownTimeout)
	defer t.Stop()
	select {
	case <-idle:
	case <-t.C:
		close(l.abandon)
		<-idle
	}
}

func (s *Subscriber) claimLoop(ctx context.Context, topic string, out chan<- *message.Message, l *loop) {
	for {
		// 至少等到一个空位，再把其余空位一起用于这次领取。
		select {
		case l.slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		n := 1
	fill:
		for n < s.cfg.Concurrency {
			select {
			case l.slots <- struct{}{}:
				n++
			default:
				break fill
			}
		}

		claims, err := s.driver.Claim(ctx, ClaimRequest{Topic: topic, Max: n, Lease: s.cfg.Lease, PollInterval: s.cfg.PollInterval})
		for range n - len(claims) {
			<-l.slots
		}
		if err != nil && ctx.Err() == nil {
			s.logger.Error("claim jobs failed", "topic", topic, "error", err)
			wait(ctx, s.cfg.PollInterval)
		}
		for i, c := range claims {
			if !s.deliver(ctx, out, c, l) {
				for _, rest := range claims[i:] {
					s.release(rest)
					<-l.slots
				}
				return
			}
		}
		if ctx.Err() != nil {
			return
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

// deliver 把任务交给 Router 并开始跟踪它的处理结果；停止领取时返回 false，任务由调用方释放。
func (s *Subscriber) deliver(ctx context.Context, out chan<- *message.Message, c Claim, l *loop) bool {
	j := c.Job()
	msg := message.NewMessage(j.ID.String(), j.Payload)
	for k, v := range j.Metadata {
		msg.Metadata.Set(k, v)
	}
	attrs := []slog.Attr{slog.String(logging.AttrMessageID, msg.UUID), slog.Int(logging.AttrAttempt, j.Attempt)}
	for _, key := range propagated {
		if v := j.Metadata[key]; v != "" {
			attrs = append(attrs, slog.String(key, v))
		}
	}
	o := &outcome{}
	msgCtx, cancel := context.WithCancelCause(context.WithValue(logging.WithAttrs(context.Background(), attrs...), outcomeKey{}, o))
	msg.SetContext(msgCtx)

	select {
	case out <- msg:
	case <-ctx.Done():
		cancel(nil)
		return false
	}
	l.inflight.Add(1)
	s.observe(msgCtx, Event{Kind: JobStarted, Job: j})
	go s.track(msgCtx, c, msg, o, cancel, l)
	return true
}

// track 等待处理结果并写回驱动，期间按 Lease/3 续租。
// ctx 是交付时的消息 context；Router 交付后会替换消息的 context，这里不能再读取 msg.Context()。
func (s *Subscriber) track(ctx context.Context, c Claim, msg *message.Message, o *outcome, cancel context.CancelCauseFunc, l *loop) {
	defer l.inflight.Done()
	defer func() { <-l.slots }()
	defer cancel(nil)

	topic := c.Job().Topic
	started := time.Now()
	ticker := time.NewTicker(max(s.cfg.Lease/3, time.Millisecond))
	defer ticker.Stop()
	held := true
	for {
		select {
		case <-msg.Acked():
			if held {
				s.ack(ctx, c, started)
			}
			return
		case <-msg.Nacked():
			if held {
				s.nack(ctx, c, o.err, started)
			}
			return
		case <-ticker.C:
			if !held {
				continue
			}
			err := withTimeout(func(ctx context.Context) error { return c.Extend(ctx, s.cfg.Lease) })
			switch {
			case errors.Is(err, ErrLeaseLost):
				held = false
				s.logger.WarnContext(ctx, "job lease lost", "topic", topic)
				s.observe(ctx, Event{Kind: JobLeaseLost, Job: c.Job(), Duration: time.Since(started)})
				cancel(ErrLeaseLost)
			case err != nil:
				s.logger.ErrorContext(ctx, "extend lease failed", "topic", topic, "error", err)
			}
		case <-l.abandon:
			if held && s.release(c) {
				s.observe(ctx, Event{Kind: JobReleased, Job: c.Job(), Duration: time.Since(started)})
			}
			cancel(ErrShuttingDown)
			return
		}
	}
}

// withTimeout 以 dbTimeout 调用 fn，不受 Router 和处理函数的 context 影响。
func withTimeout(fn func(ctx context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	return fn(ctx)
}

func (s *Subscriber) ack(ctx context.Context, c Claim, started time.Time) {
	err := withTimeout(c.Ack)
	switch {
	case err == nil:
		s.observe(ctx, Event{Kind: JobSucceeded, Job: c.Job(), Duration: time.Since(started)})
	case errors.Is(err, ErrLeaseLost):
		s.logger.WarnContext(ctx, "job lease lost before acknowledgement", "topic", c.Job().Topic)
		s.observe(ctx, Event{Kind: JobLeaseLost, Job: c.Job(), Duration: time.Since(started)})
	default:
		s.logger.ErrorContext(ctx, "acknowledge finished job failed", "topic", c.Job().Topic, "error", err)
	}
}

func (s *Subscriber) nack(ctx context.Context, c Claim, cause error, started time.Time) {
	j := c.Job()
	if cause == nil {
		cause = errors.New("queue: message nacked")
	}
	if !IsPermanent(cause) && j.Attempt < s.cfg.MaxAttempts {
		delay := retryDelay(cause, j.Attempt)
		at := time.Now().Add(delay)
		err := withTimeout(func(ctx context.Context) error { return c.Retry(ctx, at) })
		if err != nil {
			s.logger.ErrorContext(ctx, "schedule retry failed", "topic", j.Topic, "error", err)
			return
		}
		s.logger.WarnContext(ctx, "job failed, will retry", "topic", j.Topic, "retry_in", delay, "error", cause)
		s.observe(ctx, Event{Kind: JobRetrying, Job: j, Duration: time.Since(started), Err: cause, RetryAt: at})
		return
	}
	if err := withTimeout(func(ctx context.Context) error { return s.fail(ctx, c, cause) }); err != nil {
		s.logger.ErrorContext(ctx, "move job to failed jobs failed", "topic", j.Topic, "error", err)
		return
	}
	s.logger.ErrorContext(ctx, "job failed", "topic", j.Topic, "error", cause)
	s.observe(ctx, Event{Kind: JobFailed, Job: j, Duration: time.Since(started), Err: cause})
}

// failer 由能在一个事务中删除任务并写入失败记录的领取实现（数据库驱动）。
type failer interface {
	fail(ctx context.Context, record failedJob) error
}

// fail 把任务转入失败任务表。数据库驱动在一个事务中完成；其他驱动先写失败记录再确认任务，
// 两步之间进程崩溃时任务会被重新执行，再次失败时替换这条记录。
func (s *Subscriber) fail(ctx context.Context, c Claim, cause error) error {
	j := c.Job()
	record := failedJob{
		ID: j.ID, Driver: s.driver.Name(), Topic: j.Topic, Payload: j.Payload, Metadata: j.Metadata,
		Attempts: j.Attempt, Error: fmt.Sprintf("%+v", cause), FailedAt: time.Now().UTC(),
	}
	if f, ok := c.(failer); ok {
		return f.fail(ctx, record)
	}
	if err := s.failed.record(ctx, record); err != nil {
		return err
	}
	return c.Ack(ctx)
}

// release 把领取的任务还回队列，立即可被领取，本次领取不计入处理次数。返回是否还回成功。
func (s *Subscriber) release(c Claim) bool {
	err := withTimeout(c.Release)
	if err != nil && !errors.Is(err, ErrLeaseLost) {
		s.logger.Error("release job failed", "topic", c.Job().Topic, logging.AttrMessageID, c.Job().ID.String(), "error", err)
	}
	return err == nil
}
