package queue

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/ryan053857/mygoravel/logging"
)

var (
	// ErrLeaseLost 是处理函数 context 的取消原因：任务的租约已过期并可能被其他实例领取，应停止处理。
	ErrLeaseLost = errors.New("queue: lease lost")
	// ErrShuttingDown 是处理函数 context 的取消原因：停止等待超时，任务已释放给其他实例。
	ErrShuttingDown = errors.New("queue: shutting down")
)

// dbTimeout 是领取、确认和释放任务时单条语句的超时。这些语句不使用 Router 的 context，
// 停止过程中仍能写回处理结果。
const dbTimeout = 30 * time.Second

// Subscriber 从 queue_jobs 表领取到期的任务，实现 message.Subscriber。
//
// 每次 Subscribe 为一个 topic 启动一个领取循环，同时处理的任务不超过 Config.Concurrency。
// 处理函数 ack 后删除任务；nack 后按退避时间重试，次数用完或错误被 Permanent 标记时转入 queue_failed_jobs。
// 处理期间自动续租，租约丢失时取消处理函数的 context，原因是 ErrLeaseLost。
//
// Subscribe 的 ctx 取消（Router 停止）或调用 Close 后，先停止领取，再等待处理中的任务完成，
// 最多等待 Config.ShutdownTimeout；超时仍未完成的任务立即释放给其他实例，处理函数的 context 以 ErrShuttingDown 取消。
// Router 停止时取消的 context 不会传给处理函数。
//
// Router 的 Run 在处理函数返回后就会返回，此时处理结果可能还没有写回数据库。
// 在 Run 返回之后、关闭数据库之前调用 Close，等待写回完成：
//
//	sub := queue.NewSubscriber(db, cfg, logger)
//	defer sub.Close()
type Subscriber struct {
	db     *bun.DB
	cfg    Config
	logger *slog.Logger

	mu     sync.Mutex
	closed bool
	loops  sync.WaitGroup // 每个 topic 的领取循环，包括停止时等待处理中的任务

	closing   chan struct{} // 停止领取
	done      chan struct{}
	closeOnce sync.Once
}

var _ message.Subscriber = (*Subscriber)(nil)

// NewSubscriber 创建订阅者。db 必须已执行 Migrations 中的迁移；logger 记录任务失败和数据库错误，
// nil 时使用 slog.Default()。cfg 不合法时 panic。
func NewSubscriber(db *bun.DB, cfg Config, logger *slog.Logger) *Subscriber {
	if db == nil {
		panic("queue: nil database")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "queue"))
	}
	return &Subscriber{
		db:      db,
		cfg:     cfg,
		logger:  logging.Or(logger).With(logging.AttrComponent, "queue"),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
	}
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
	slots := l.slots
	stop := func() bool {
		select {
		case <-ctx.Done():
			return true
		case <-s.closing:
			return true
		default:
			return false
		}
	}
	wait := func(d time.Duration) {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
		case <-s.closing:
		}
	}

	for {
		// 至少等到一个空位，再把其余空位一起用于这次领取。
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		case <-s.closing:
			return
		}
		n := 1
	fill:
		for n < s.cfg.Concurrency {
			select {
			case slots <- struct{}{}:
				n++
			default:
				break fill
			}
		}

		jobs, err := s.claim(topic, n)
		for range n - len(jobs) {
			<-slots
		}
		if err != nil {
			s.logger.Error("claim jobs failed", "topic", topic, "error", err)
			wait(s.cfg.PollInterval)
			continue
		}
		for i := range jobs {
			if !s.deliver(ctx, out, jobs[i], l) {
				for _, rest := range jobs[i:] {
					s.release(rest)
					<-slots
				}
				return
			}
		}
		if len(jobs) < n && !stop() {
			wait(s.cfg.PollInterval)
		}
	}
}

// deliver 把任务交给 Router 并开始跟踪它的处理结果；停止领取时返回 false，任务由调用方释放。
func (s *Subscriber) deliver(ctx context.Context, out chan<- *message.Message, j job, l *loop) bool {
	msg := message.NewMessage(j.ID.String(), j.Payload)
	for k, v := range j.Metadata {
		msg.Metadata.Set(k, v)
	}
	attrs := []slog.Attr{slog.String(logging.AttrMessageID, msg.UUID), slog.Int(logging.AttrAttempt, j.Attempts)}
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
	case <-s.closing:
		cancel(nil)
		return false
	}
	l.inflight.Add(1)
	go s.track(msgCtx, j, msg, o, cancel, l)
	return true
}

// track 等待处理结果并写回数据库，期间按 Lease/3 续租。
// ctx 是交付时的消息 context；Router 交付后会替换消息的 context，这里不能再读取 msg.Context()。
func (s *Subscriber) track(ctx context.Context, j job, msg *message.Message, o *outcome, cancel context.CancelCauseFunc, l *loop) {
	defer l.inflight.Done()
	defer func() { <-l.slots }()
	defer cancel(nil)

	ticker := time.NewTicker(max(s.cfg.Lease/3, time.Millisecond))
	defer ticker.Stop()
	held := true
	for {
		select {
		case <-msg.Acked():
			if held {
				s.ack(ctx, j)
			}
			return
		case <-msg.Nacked():
			if held {
				s.nack(ctx, j, o.err)
			}
			return
		case <-ticker.C:
			if !held {
				continue
			}
			ok, err := s.update(j, map[string]any{"available_at": time.Now().UTC().Add(s.cfg.Lease)})
			if err != nil {
				s.logger.ErrorContext(ctx, "extend lease failed", "topic", j.Topic, "error", err)
			} else if !ok {
				held = false
				s.logger.WarnContext(ctx, "job lease lost", "topic", j.Topic)
				cancel(ErrLeaseLost)
			}
		case <-l.abandon:
			if held {
				s.release(j)
			}
			cancel(ErrShuttingDown)
			return
		}
	}
}

// claim 领取 topic 中最多 n 个到期的任务：attempts 加一，available_at 推到租约到期时间。
func (s *Subscriber) claim(topic string, n int) ([]job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	now := time.Now().UTC()
	ready := func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("j.topic = ?", topic).Where("j.available_at <= ?", now).
			OrderExpr("j.available_at, j.id").Limit(int64(n))
	}

	if s.db.Dialect().Name() == dialect.MySQL {
		jobs, err := s.claimMySQL(ctx, ready, now)
		return jobs, errors.Wrapf(err, "queue: claim %q", topic)
	}
	ids := ready(s.db.NewSelect().Model((*job)(nil)).Column("j.id"))
	// PostgreSQL 用 SKIP LOCKED 跳过其他实例正在领取的行；SQLite 同一时刻只有一个写者，整条 UPDATE 本身就是互斥的。
	if s.db.Dialect().Name() == dialect.PG {
		ids = ids.For("UPDATE SKIP LOCKED")
	}
	var jobs []job
	err := s.db.NewUpdate().Model((*job)(nil)).
		Set("available_at = ?", now.Add(s.cfg.Lease)).
		Set("reserved_at = ?", now).
		Set("attempts = j.attempts + 1").
		Where("j.id IN (?)", ids).
		Returning("*").
		Scan(ctx, &jobs)
	return jobs, errors.Wrapf(err, "queue: claim %q", topic)
}

// claimMySQL 在一个事务中先锁住到期的行再更新：MySQL 没有 UPDATE … RETURNING，
// 也不允许在 IN 子查询中使用 LIMIT。读已提交隔离级别下 FOR UPDATE 只锁返回的行，不加间隙锁，
// 不会挡住同时发布的任务。
func (s *Subscriber) claimMySQL(ctx context.Context, ready func(*bun.SelectQuery) *bun.SelectQuery, now time.Time) ([]job, error) {
	var jobs []job
	err := s.db.RunInTx(ctx, txOptions(s.db), func(ctx context.Context, tx bun.Tx) error {
		if err := ready(tx.NewSelect().Model(&jobs)).For("UPDATE SKIP LOCKED").Scan(ctx); err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(jobs))
		for i := range jobs {
			ids[i] = jobs[i].ID
		}
		lease := now.Add(s.cfg.Lease)
		_, err := tx.NewUpdate().Model((*job)(nil)).
			Set("available_at = ?", lease).
			Set("reserved_at = ?", now).
			Set("attempts = j.attempts + 1").
			Where("j.id IN (?)", bun.In(ids)).
			Exec(ctx)
		for i := range jobs {
			jobs[i].Attempts++
			jobs[i].AvailableAt = lease
			jobs[i].ReservedAt = now
		}
		return err
	})
	return jobs, err
}

func (s *Subscriber) ack(ctx context.Context, j job) {
	dbCtx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	_, err := s.db.NewDelete().Model((*job)(nil)).
		Where("id = ?", j.ID).Where("attempts = ?", j.Attempts).Exec(dbCtx)
	if err != nil {
		s.logger.ErrorContext(ctx, "delete finished job failed", "topic", j.Topic, "error", err)
	}
}

func (s *Subscriber) nack(ctx context.Context, j job, cause error) {
	if cause == nil {
		cause = errors.New("queue: message nacked")
	}
	if !IsPermanent(cause) && j.Attempts < s.cfg.MaxAttempts {
		delay := backoff(j.Attempts)
		_, err := s.update(j, map[string]any{"available_at": time.Now().UTC().Add(delay), "reserved_at": nil})
		if err != nil {
			s.logger.ErrorContext(ctx, "schedule retry failed", "topic", j.Topic, "error", err)
			return
		}
		s.logger.WarnContext(ctx, "job failed, will retry", "topic", j.Topic, "retry_in", delay, "error", cause)
		return
	}
	if err := s.fail(j, cause); err != nil {
		s.logger.ErrorContext(ctx, "move job to failed jobs failed", "topic", j.Topic, "error", err)
		return
	}
	s.logger.ErrorContext(ctx, "job failed", "topic", j.Topic, "error", cause)
}

// fail 在一个事务中把仍由本实例持有的任务移入 queue_failed_jobs。
func (s *Subscriber) fail(j job, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	return s.db.RunInTx(ctx, txOptions(s.db), func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewDelete().Model((*job)(nil)).
			Where("id = ?", j.ID).Where("attempts = ?", j.Attempts).Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return err // 租约已丢失，任务由其他实例处理
		}
		row := &failedJob{
			ID: j.ID, Driver: driverDatabase, Topic: j.Topic, Payload: j.Payload, Metadata: j.Metadata,
			Attempts: j.Attempts, Error: fmt.Sprintf("%+v", cause), FailedAt: time.Now().UTC(),
		}
		// 同一个任务之前失败过、重新入队后又失败时，替换旧的失败记录。
		if _, err := tx.NewDelete().Model((*failedJob)(nil)).Where("id = ?", j.ID).Exec(ctx); err != nil {
			return err
		}
		_, err = tx.NewInsert().Model(row).Exec(ctx)
		return err
	})
}

// release 把领取的任务还回队列，立即可被领取，本次领取不计入处理次数。
func (s *Subscriber) release(j job) {
	_, err := s.update(j, map[string]any{"available_at": time.Now().UTC(), "reserved_at": nil, "attempts": j.Attempts - 1})
	if err != nil {
		s.logger.Error("release job failed", "topic", j.Topic, logging.AttrMessageID, j.ID.String(), "error", err)
	}
}

// update 在任务仍由本次领取持有时（attempts 未变）更新 set 中的列，返回是否更新成功。
func (s *Subscriber) update(j job, set map[string]any) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	q := s.db.NewUpdate().Model((*job)(nil)).Where("id = ?", j.ID).Where("attempts = ?", j.Attempts)
	for col, v := range set {
		q = q.Set("? = ?", bun.Ident(col), v)
	}
	res, err := q.Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
