package schedule

import (
	"context"
	"time"

	"github.com/ryan053857/mygoravel/monitor"
)

// WithMonitor 记录的运行统计，label 都是任务名。耗时以秒记录。
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

// WithMonitor 让调度器把每个任务的执行次数、耗时和上一次执行的结果记录到 rec，供命令和后台页面查看，
// 用 Activity 和 LastRuns 查询。它是一个 Observer，只在运行调度器的进程中记录。rec 为 nil 时 panic。
func WithMonitor(rec *monitor.Recorder) SchedulerOption {
	if rec == nil {
		panic("schedule: nil recorder")
	}
	return WithObserver(func(_ context.Context, e Event) {
		switch e.Kind {
		case TaskSucceeded, TaskFailed:
			run := Run{Tick: e.Tick, Started: e.Started, Duration: e.Duration}
			metric := MetricSucceeded
			if e.Kind == TaskFailed {
				metric = MetricFailed
				run.Error = e.Err.Error()
				if len(run.Error) > maxErrorLength {
					run.Error = run.Error[:maxErrorLength]
				}
			}
			rec.Record(metric, e.Task, e.Duration.Seconds())
			rec.Set(MetricLastRun, e.Task, run)
		case TaskSkipped:
			rec.Record(MetricSkipped, e.Task, 1)
		}
	})
}

// TaskActivity 是一个任务在一段时间内的执行情况。
type TaskActivity struct {
	Succeeded, Failed, Skipped int64
	// MeanDuration、MaxDuration 是成功和失败的执行的平均和最长耗时。
	MeanDuration, MaxDuration time.Duration
}

// Activity 返回 WithMonitor 记录的每个任务在 from 到 to 之间的执行情况，键是任务名。
// 时间范围的含义见 monitor.Store.Totals。
func Activity(ctx context.Context, stats *monitor.Store, from, to time.Time) (map[string]TaskActivity, error) {
	out := map[string]TaskActivity{}
	durations := map[string]monitor.Total{}
	for _, metric := range []string{MetricSucceeded, MetricFailed, MetricSkipped} {
		totals, err := stats.Totals(ctx, metric, from, to)
		if err != nil {
			return nil, err
		}
		for _, t := range totals {
			a := out[t.Label]
			switch metric {
			case MetricSucceeded:
				a.Succeeded = t.Count
			case MetricFailed:
				a.Failed = t.Count
			case MetricSkipped:
				a.Skipped = t.Count
			}
			if metric != MetricSkipped {
				d := durations[t.Label]
				d.Count, d.Sum, d.Max = d.Count+t.Count, d.Sum+t.Sum, max(d.Max, t.Max)
				durations[t.Label] = d
			}
			out[t.Label] = a
		}
	}
	for task, d := range durations {
		a := out[task]
		a.MeanDuration, a.MaxDuration = seconds(d.Mean()), seconds(d.Max)
		out[task] = a
	}
	return out, nil
}

// LastRuns 返回 WithMonitor 记录的每个任务上一次执行的结果，键是任务名，没有执行过的任务不在其中。
// 与 Task.Prev 比较，可以发现应当执行却没有执行的触发。
func LastRuns(ctx context.Context, stats *monitor.Store) (map[string]Run, error) {
	values, err := stats.Latest(ctx, MetricLastRun)
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

// seconds 把秒数转换为 time.Duration。
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
