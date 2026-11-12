package schedule

import (
	"context"
	"time"
)

// EventKind 是执行事件的类型。
type EventKind int

const (
	// TaskStarted 表示本实例开始执行任务。
	TaskStarted EventKind = iota + 1
	// TaskSucceeded 表示任务执行完成。
	TaskSucceeded
	// TaskFailed 表示任务返回错误或 panic，Event.Err 是原因。
	TaskFailed
	// TaskSkipped 表示任务应当在本实例执行却被跳过，Event.SkipReason 是原因。由其他实例执行的触发不报告。
	TaskSkipped
)

func (k EventKind) String() string {
	switch k {
	case TaskStarted:
		return "started"
	case TaskSucceeded:
		return "succeeded"
	case TaskFailed:
		return "failed"
	case TaskSkipped:
		return "skipped"
	}
	return "unknown"
}

// SkipReason 是任务被跳过的原因。
type SkipReason string

const (
	// SkipOverlap 表示上一次还没结束，在任何一个实例上都算。
	SkipOverlap SkipReason = "overlap"
	// SkipLockFailed 表示锁存储出错，无法确认由谁执行，Event.Err 是错误。
	SkipLockFailed SkipReason = "lock_failed"
)

// Event 是一次执行的状态变化。
type Event struct {
	Kind EventKind
	Task string
	// Tick 是这次触发的时刻，即任务应当执行的时间，与 Task.Prev 的结果对应。
	Tick time.Time
	// Started 是实际开始执行的时间，TaskSkipped 为零值。Started 减去 Tick 是调度延迟。
	Started time.Time
	// Duration 是执行耗时，只用于 TaskSucceeded 和 TaskFailed。
	Duration time.Duration
	// Err 是 TaskFailed 和 SkipLockFailed 的原因。
	Err        error
	SkipReason SkipReason
}

// Observer 接收执行事件，例如把执行记录写入数据库，供后台页面查看。它在执行任务的 goroutine 中同步调用，
// ctx 带有任务的日志字段和链路。Observer 中的 panic 会被恢复并记录。
type Observer func(ctx context.Context, e Event)

// WithObserver 为调度器添加 Observer，可以多次使用。o 为 nil 时 panic。
func WithObserver(o Observer) SchedulerOption {
	if o == nil {
		panic("schedule: nil observer")
	}
	return func(s *Scheduler) { s.observers = append(s.observers, o) }
}

// observe 依次调用 Observer。
func (s *Scheduler) observe(ctx context.Context, e Event) {
	for _, o := range s.observers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.ErrorContext(ctx, "schedule observer panicked", "event", e.Kind.String(), "panic", r)
				}
			}()
			o(ctx, e)
		}()
	}
}
