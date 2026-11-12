package schedule

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/ryan053857/mygoravel/monitor"
)

// WithMonitor 记录的运行统计，label 都是任务名。
const (
	// MetricSucceeded 是执行完成的次数和耗时。
	MetricSucceeded = "schedule.succeeded"
	// MetricFailed 是返回错误或 panic 的次数和耗时。
	MetricFailed = "schedule.failed"
	// MetricSkipped 是应当在某个实例上执行却被跳过的次数。
	MetricSkipped = "schedule.skipped"
	// MetricLastRun 是每个任务上一次执行的 Run。
	MetricLastRun = "schedule.last_run"
)

// maxErrorLength 是 Run.Error 保存的最大字节数。
const maxErrorLength = 4096

// Run 是一次执行的结果。
type Run struct {
	// Tick 是这次触发的时刻，Started 是实际开始的时间。
	Tick     time.Time     `json:"tick"`
	Started  time.Time     `json:"started"`
	Duration time.Duration `json:"duration"`
	// Error 是失败的原因，成功时为空，最长 4096 字节。
	Error string `json:"error,omitempty"`
}

// Failed 报告这次执行是否失败。
func (r Run) Failed() bool { return r.Error != "" }

// WithMonitor 让调度器把每个任务的执行次数、耗时和上一次执行的结果记录到 m，供命令和后台页面查看，
// 用 Activity 和 LastRuns 查询。它是一个 Observer，只在运行调度器的进程中记录。m 为 nil 时 panic。
func WithMonitor(m *monitor.Monitor) SchedulerOption {
	if m == nil {
		panic("schedule: nil monitor")
	}
	return WithObserver(func(_ context.Context, e Event) {
		switch e.Kind {
		case TaskSucceeded:
			m.RecordDuration(MetricSucceeded, e.Task, e.Duration)
			m.Set(MetricLastRun, e.Task, Run{Tick: e.Tick, Started: e.Started, Duration: e.Duration})
		case TaskFailed:
			m.RecordDuration(MetricFailed, e.Task, e.Duration)
			m.Set(MetricLastRun, e.Task, Run{Tick: e.Tick, Started: e.Started, Duration: e.Duration, Error: truncate(e.Err.Error())})
		case TaskSkipped:
			m.Record(MetricSkipped, e.Task, 1)
		}
	})
}

// truncate 把 s 截断到 maxErrorLength 字节以内，不切断字符。
func truncate(s string) string {
	if len(s) <= maxErrorLength {
		return s
	}
	i := maxErrorLength
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// TaskActivity 是一个任务在一段时间内的执行情况。
type TaskActivity struct {
	// Succeeded、Failed 是执行完成和失败的次数，它们的 MeanDuration、MaxDuration 是耗时。
	Succeeded, Failed monitor.Aggregate
	// Skipped 是被跳过的次数。
	Skipped int64
}

// Executions 返回全部执行的汇总，用来计算总的平均和最长耗时。
func (a TaskActivity) Executions() monitor.Aggregate { return a.Succeeded.Add(a.Failed) }

// Activity 返回 WithMonitor 记录的每个任务在 from 到 to 之间的执行情况，键是任务名。
// 时间范围的含义见 monitor.Monitor.Totals。
func Activity(ctx context.Context, m *monitor.Monitor, from, to time.Time) (map[string]TaskActivity, error) {
	totals, err := m.Totals(ctx, from, to, MetricSucceeded, MetricFailed, MetricSkipped)
	if err != nil {
		return nil, err
	}
	out := map[string]TaskActivity{}
	for _, t := range totals {
		a := out[t.Label]
		switch t.Metric {
		case MetricSucceeded:
			a.Succeeded = t.Aggregate
		case MetricFailed:
			a.Failed = t.Aggregate
		case MetricSkipped:
			a.Skipped = t.Count
		}
		out[t.Label] = a
	}
	return out, nil
}

// LastRuns 返回 WithMonitor 记录的每个任务上一次执行的结果，键是任务名，没有执行过的任务不在其中。
// 与 Task.Prev 比较，可以发现应当执行却没有执行的触发。
func LastRuns(ctx context.Context, m *monitor.Monitor) (map[string]Run, error) {
	values, err := m.Latest(ctx, MetricLastRun)
	if err != nil {
		return nil, err
	}
	runs := make(map[string]Run, len(values))
	for _, v := range values {
		var r Run
		if err := v.Decode(&r); err != nil {
			return nil, err
		}
		runs[v.Label] = r
	}
	return runs, nil
}
