package schedule

import (
	"context"
	"time"

	"github.com/adhocore/gronx"
	"github.com/cockroachdb/errors"
)

var (
	// ErrUnknownTask 表示调度器中没有这个名称的任务。
	ErrUnknownTask = errors.New("schedule: unknown task")
	// ErrNotTracked 表示无法跨实例判断任务是否正在执行：使用 EveryInstance 或 AllowOverlap 的任务
	// 不在锁存储中登记执行状态。
	ErrNotTracked = errors.New("schedule: task running state is not tracked")
)

// Describe 为任务添加一句说明，供 Tasks 列出，例如在后台页面和命令中展示。
func Describe(description string) TaskOption { return func(t *task) { t.description = description } }

// Task 描述一个注册的任务，由 Tasks 返回。
type Task struct {
	Name        string
	Description string
	// Expr 是 Add 时传入的表达式。
	Expr string
	// Location 是计算 cron 表达式使用的时区。
	Location      *time.Location
	EveryInstance bool
	AllowOverlap  bool

	when when
}

// Next 返回 after 之后（不含 after 所在的这一秒）任务下一次到期的时刻，时区为 Location。
// 表达式不会再到期时（例如指定了已经过去的年份）返回零值。
func (t Task) Next(after time.Time) time.Time { return t.when.next(after, t.Location) }

// Prev 返回 before 或之前任务最后一次到期的时刻，时区为 Location。与执行记录比较，可以发现应当执行却没有执行的触发，
// 例如调度器停止或部署期间错过的触发。表达式从未到期时返回零值。
func (t Task) Prev(before time.Time) time.Time { return t.when.prev(before, t.Location) }

// Tasks 按注册顺序返回全部任务。它只读取任务的定义，不需要运行调度器：
// HTTP 进程和命令可以创建同样的调度器，用它展示任务和下一次执行时间。
func (s *Scheduler) Tasks() []Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Task, len(s.tasks))
	for i, t := range s.tasks {
		out[i] = Task{
			Name: t.name, Description: t.description, Expr: t.expr, Location: t.loc,
			EveryInstance: t.everyInstance, AllowOverlap: t.allowOverlap, when: t.when,
		}
	}
	return out
}

// Running 报告任务此刻是否正在执行，在任何一个实例上都算。它查询锁存储中任务执行期间持有的锁，
// 所以可以在没有运行调度器的进程中调用，结果的范围取决于锁存储：多实例部署需要共用数据库或 Redis 存储。
// 任务不存在时返回 ErrUnknownTask；使用 EveryInstance 或 AllowOverlap 的任务返回 ErrNotTracked。
func (s *Scheduler) Running(ctx context.Context, name string) (bool, error) {
	s.mu.Lock()
	var found *task
	for _, t := range s.tasks {
		if t.name == name {
			found = t
		}
	}
	s.mu.Unlock()
	switch {
	case found == nil:
		return false, errors.Wrapf(ErrUnknownTask, "%q", name)
	case found.everyInstance || found.allowOverlap:
		return false, errors.Wrapf(ErrNotTracked, "%q", name)
	}
	return s.locker.Held(ctx, runningKey(name))
}

// runningKey 是任务执行期间持有的锁。
func runningKey(name string) string { return "schedule:" + name + ":running" }

// next 返回 after 所在的秒之后第一个到期的时刻。
func (w when) next(after time.Time, loc *time.Location) time.Time {
	if w.every > 0 {
		n := int64(w.every / time.Second)
		return time.Unix((after.Unix()/n+1)*n, 0).In(loc)
	}
	t, err := gronx.NextTickAfter(w.cron, after.In(loc).Truncate(time.Second), false)
	if err != nil {
		return time.Time{}
	}
	return t
}

// prev 返回 before 所在的秒或之前最后一个到期的时刻。
func (w when) prev(before time.Time, loc *time.Location) time.Time {
	if w.every > 0 {
		n := int64(w.every / time.Second)
		return time.Unix(before.Unix()/n*n, 0).In(loc)
	}
	t, err := gronx.PrevTickBefore(w.cron, before.In(loc).Truncate(time.Second), true)
	if err != nil {
		return time.Time{}
	}
	return t
}
