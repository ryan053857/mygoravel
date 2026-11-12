package queue

import (
	"context"
	"time"
)

// EventKind 是任务事件的类型。
type EventKind int

const (
	// JobStarted 表示任务已交给处理函数。
	JobStarted EventKind = iota + 1
	// JobSucceeded 表示任务处理成功并已确认。
	JobSucceeded
	// JobRetrying 表示任务处理失败，已安排在 Event.RetryAt 重试。
	JobRetrying
	// JobFailed 表示任务永久失败或次数用完，已转入失败任务表。
	JobFailed
	// JobReleased 表示停止等待超时，任务已还回队列。
	JobReleased
	// JobLeaseLost 表示任务的租约已过期，任务可能已被其他实例领取。
	JobLeaseLost
)

func (k EventKind) String() string {
	switch k {
	case JobStarted:
		return "started"
	case JobSucceeded:
		return "succeeded"
	case JobRetrying:
		return "retrying"
	case JobFailed:
		return "failed"
	case JobReleased:
		return "released"
	case JobLeaseLost:
		return "lease_lost"
	}
	return "unknown"
}

// Event 是 Worker 报告的一次任务状态变化。除 JobStarted 外，事件都在结果写回驱动之后报告。
type Event struct {
	Kind   EventKind
	Driver string
	Job    Job
	// Started 是任务交给处理函数的时间。
	Started time.Time
	// Duration 是从交给处理函数到结束的时间；JobStarted 为 0。
	Duration time.Duration
	// Err 是 JobRetrying 和 JobFailed 的原因。
	Err error
	// RetryAt 是 JobRetrying 的下一次执行时间。
	RetryAt time.Time
}

// Observer 接收任务事件，例如记录指标、写入执行历史。它在处理任务的 goroutine 中同步调用，
// 耗时会占用并发名额：耗时的工作请缓冲或异步完成。ctx 带有任务的日志字段。Observer 中的 panic 会被恢复并记录。
type Observer func(ctx context.Context, e Event)

// WithObserver 为 Worker 添加 Observer，可以多次使用。o 为 nil 时 panic。
func WithObserver(o Observer) WorkerOption {
	if o == nil {
		panic("queue: nil observer")
	}
	return func(w *Worker) { w.observers = append(w.observers, o) }
}

// observe 依次调用 Observer。
func (w *Worker) observe(ctx context.Context, e Event) {
	e.Driver = w.driver.Name()
	for _, o := range w.observers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					w.logger.ErrorContext(ctx, "queue observer panicked", "event", e.Kind.String(), "panic", r)
				}
			}()
			o(ctx, e)
		}()
	}
	w.endSpan(ctx, e)
}
