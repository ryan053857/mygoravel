package schedule

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"
	"strconv"
	"sync"
	"time"

	"github.com/adhocore/gronx"
	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/logging"
)

const (
	// tickTTL 是“这一次触发由谁执行”的锁的有效期。各实例的时钟偏差小于它时不会重复执行。
	tickTTL = time.Minute
	// runningTTL 是“任务正在执行”的锁的有效期，执行期间每 runningTTL/3 续期。
	runningTTL = time.Minute
	// maxLag 是调度循环落后时补算的最长时间，超过后跳过错过的触发。
	maxLag = time.Minute
)

// MaxNameLength 是任务名的最大字节数。调度器的锁名是 "schedule:<任务名>:<触发时刻的 Unix 秒>"，
// 不能超过 lock.MaxNameLength；Unix 秒按 int64 最长的 20 个字符预留。
const MaxNameLength = lock.MaxNameLength - len("schedule:") - len(":") - 20

// Scheduler 按表达式定时执行任务。用 Add 注册任务，再调用 Run。
type Scheduler struct {
	locker          *lock.Locker
	logger          *slog.Logger
	shutdownTimeout time.Duration
	observers       []Observer
	tracer          trace.Tracer // 为 nil 时不创建 span

	mu      sync.Mutex
	tasks   []*task
	started bool
}

type task struct {
	name          string
	description   string
	expr          string
	when          when
	run           func(ctx context.Context) error
	loc           *time.Location
	everyInstance bool
	allowOverlap  bool

	mu      sync.Mutex
	running bool // 本进程中是否正在执行
}

// TaskOption 调整 Add 注册的任务。
type TaskOption func(*task)

// EveryInstance 让任务在每个实例上都执行。默认整个集群在同一次触发只执行一次。
func EveryInstance() TaskOption { return func(t *task) { t.everyInstance = true } }

// AllowOverlap 允许任务在上一次还没结束时再次执行。默认跳过本次。
func AllowOverlap() TaskOption { return func(t *task) { t.allowOverlap = true } }

// In 按 loc 计算 cron 表达式，默认使用 time.Local（可用 TZ 环境变量设置）。对 @every 没有影响。
func In(loc *time.Location) TaskOption { return func(t *task) { t.loc = loc } }

// SchedulerOption 调整 New 创建的调度器。
type SchedulerOption func(*Scheduler)

// New 创建调度器。locker 用于多实例去重和防止重叠，所有实例共用数据库或 Redis 锁存储；
// 测试中可以使用 locktest.NewStore。logger 为 nil 时使用 slog.Default()。
// locker 为 nil 或 cfg 不合法时 panic。
func New(locker *lock.Locker, cfg Config, logger *slog.Logger, opts ...SchedulerOption) *Scheduler {
	if locker == nil {
		panic("schedule: nil locker")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "schedule"))
	}
	s := &Scheduler{
		locker:          locker,
		logger:          logging.Or(logger).With(logging.AttrComponent, "schedule"),
		shutdownTimeout: cfg.ShutdownTimeout,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Add 注册任务。name 在调度器中唯一，也用作锁名的一部分，最长 MaxNameLength 字节；expr 支持
// 5 段 cron 表达式（分钟精度，在第 0 秒触发）、6 段（第一段是秒）、@hourly、@daily 这类预定义写法，
// 以及按 Unix 时间对齐的 "@every 15s"（至少 1 秒，必须是整秒）。
// 名称为空、过长或重复，表达式非法，或者在 Run 之后调用时 panic。
func (s *Scheduler) Add(name, expr string, run func(ctx context.Context) error, opts ...TaskOption) {
	if name == "" || run == nil {
		panic("schedule: task name and function are required")
	}
	if len(name) > MaxNameLength {
		panic(errors.Newf("schedule: task name %.20q… is longer than %d bytes", name, MaxNameLength))
	}
	w, err := parse(expr)
	if err != nil {
		panic(err)
	}
	t := &task{name: name, expr: expr, when: w, run: run, loc: time.Local}
	for _, opt := range opts {
		opt(t)
	}
	if t.loc == nil {
		panic(errors.Newf("schedule: nil location for task %q", name))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		panic("schedule: Add after Run")
	}
	for _, existing := range s.tasks {
		if existing.name == name {
			panic(errors.Newf("schedule: duplicate task %q", name))
		}
	}
	s.tasks = append(s.tasks, t)
}

// Run 每秒检查一次到期的任务并执行，直到 ctx 被取消。
//
// 调度落后（例如进程被暂停）不到一分钟时补算错过的触发，同一个任务只补执行一次；
// 落后更久时跳过错过的触发。ctx 取消后不再触发新任务，等待执行中的任务结束，
// 最多等待 Config.ShutdownTimeout，超时后取消它们的 ctx。Run 只能调用一次。
func (s *Scheduler) Run(ctx context.Context) error {
	tasks, err := s.start()
	if err != nil {
		return err
	}
	taskCtx, cancelTasks := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelTasks()
	var wg sync.WaitGroup

	g := gronx.New()
	next := time.Now().Truncate(time.Second).Add(time.Second)
	for {
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			s.wait(&wg, cancelTasks)
			return nil
		case <-timer.C:
		}

		last := time.Now().Truncate(time.Second)
		if lag := last.Sub(next); lag >= maxLag {
			s.logger.Warn("scheduler fell behind, skipped ticks", "from", next, "to", last.Add(-time.Second))
			next = last
		}
		// 每个任务在 [next, last] 中最后一次到期的时刻。
		for _, t := range tasks {
			var tick time.Time
			for at := next; !at.After(last); at = at.Add(time.Second) {
				if t.when.due(g, at, t.loc) {
					tick = at
				}
			}
			if tick.IsZero() {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := s.execute(taskCtx, t, tick); err != nil {
					s.logger.ErrorContext(taskContext(taskCtx, t), "scheduled task failed", "error", err)
				}
			}()
		}
		next = last.Add(time.Second)
	}
}

func (s *Scheduler) start() ([]*task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil, errors.New("schedule: Run called twice")
	}
	s.started = true
	return s.tasks, nil
}

// wait 等待执行中的任务结束，超过 ShutdownTimeout 时取消它们的 ctx 后继续等待。
func (s *Scheduler) wait(wg *sync.WaitGroup, cancel context.CancelFunc) {
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(s.shutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		cancel()
		<-done
	}
}

// RunDue 执行在 t 所在的这一秒到期的任务，等它们全部结束，返回合并后的错误。
// 去重和防止重叠的规则与 Run 相同。用于测试，或者由外部的 cron 每秒、每分钟调用。
func (s *Scheduler) RunDue(ctx context.Context, t time.Time) error {
	s.mu.Lock()
	tasks := s.tasks
	s.mu.Unlock()
	tick := t.Truncate(time.Second)
	g := gronx.New()
	errs := make([]error, len(tasks))
	var wg sync.WaitGroup
	for i, task := range tasks {
		if !task.when.due(g, tick, task.loc) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.execute(ctx, task, tick); err != nil {
				errs[i] = errors.Wrapf(err, "schedule: task %q", task.name)
			}
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// execute 取得这一次触发和防止重叠所需的锁，然后执行任务。
// 其他实例已经执行这一次触发、或者任务仍在执行时直接返回 nil。
func (s *Scheduler) execute(ctx context.Context, t *task, tick time.Time) error {
	ctx = taskContext(ctx, t)
	finished, err := s.runLocked(ctx, t, tick)
	if finished != nil {
		// 结束事件在释放执行锁之后发出：Observer 收到它时，Running 已经返回 false。
		s.observe(finished.ctx, finished.Event)
	}
	return err
}

// finishedRun 是一次执行的结束事件，以及发出它时使用的 ctx（带有本次执行的 span）。
type finishedRun struct {
	Event
	ctx context.Context
}

// runLocked 按去重和防重叠的规则取得锁并执行任务，返回时锁已释放；没有执行时 finished 为 nil。
func (s *Scheduler) runLocked(ctx context.Context, t *task, tick time.Time) (finished *finishedRun, _ error) {
	if !t.allowOverlap {
		if !t.begin() {
			s.logger.InfoContext(ctx, "scheduled task skipped, still running")
			s.observe(ctx, Event{Kind: TaskSkipped, Task: t.name, Tick: tick, SkipReason: SkipOverlap})
			return nil, nil
		}
		defer t.end()
	}
	if !t.everyInstance {
		key := "schedule:" + t.name + ":" + strconv.FormatInt(tick.Unix(), 10)
		if _, err := s.locker.Acquire(ctx, key, tickTTL); err != nil {
			if errors.Is(err, lock.ErrNotAcquired) {
				return nil, nil // 其他实例执行这一次触发
			}
			s.observe(ctx, Event{Kind: TaskSkipped, Task: t.name, Tick: tick, SkipReason: SkipLockFailed, Err: err})
			return nil, err
		}
		if !t.allowOverlap {
			running, err := s.locker.Acquire(ctx, runningKey(t.name), runningTTL)
			if errors.Is(err, lock.ErrNotAcquired) {
				s.logger.InfoContext(ctx, "scheduled task skipped, still running on another instance")
				s.observe(ctx, Event{Kind: TaskSkipped, Task: t.name, Tick: tick, SkipReason: SkipOverlap})
				return nil, nil
			}
			if err != nil {
				s.observe(ctx, Event{Kind: TaskSkipped, Task: t.name, Tick: tick, SkipReason: SkipLockFailed, Err: err})
				return nil, err
			}
			stop := s.keep(ctx, running)
			defer stop()
		}
	}
	ctx, end := s.startSpan(ctx, t.name, tick)
	start := time.Now()
	s.observe(ctx, Event{Kind: TaskStarted, Task: t.name, Tick: tick, Started: start})
	err := call(ctx, t.run)
	elapsed := time.Since(start)
	end(err)
	s.logger.DebugContext(ctx, "scheduled task finished", "duration", elapsed, "error", err)
	e := Event{Kind: TaskSucceeded, Task: t.name, Tick: tick, Started: start, Duration: elapsed, Err: err}
	if err != nil {
		e.Kind = TaskFailed
	}
	return &finishedRun{Event: e, ctx: ctx}, err
}

func (t *task) begin() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.running {
		return false
	}
	t.running = true
	return true
}

func (t *task) end() {
	t.mu.Lock()
	t.running = false
	t.mu.Unlock()
}

// keep 在任务执行期间续期 running 锁，返回的函数停止续期并释放锁。
func (s *Scheduler) keep(ctx context.Context, running *lock.Lock) func() {
	stopped := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(runningTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-ticker.C:
				if err := running.Refresh(ctx, runningTTL); err != nil {
					s.logger.WarnContext(ctx, "refresh scheduled task lock failed", "error", err)
				}
			}
		}
	}()
	return func() {
		close(stopped)
		<-done
		if err := running.Release(context.WithoutCancel(ctx)); err != nil {
			s.logger.WarnContext(ctx, "release scheduled task lock failed", "error", err)
		}
	}
}

// call 执行任务，把 panic 转为错误。
func call(ctx context.Context, run func(ctx context.Context) error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Newf("schedule: panic: %v\n%s", r, debug.Stack())
		}
	}()
	return run(ctx)
}

func taskContext(ctx context.Context, t *task) context.Context {
	return logging.WithAttrs(ctx, slog.String("task", t.name))
}

// Cleanup 把 Cleanup(ctx, limit) 形式的清理函数包装成任务：反复调用，直到一次删除的数量少于 limit。
// 锁、限流、会话、文件存储的清理方法都是这个形式。
func Cleanup(fn func(ctx context.Context, limit int) (int, error), limit int) func(ctx context.Context) error {
	if limit <= 0 {
		panic(fmt.Sprintf("schedule: cleanup limit must be positive, got %d", limit))
	}
	return func(ctx context.Context) error {
		for {
			n, err := fn(ctx, limit)
			if err != nil || n < limit {
				return err
			}
			if err := ctx.Err(); err != nil {
				return err
			}
		}
	}
}
