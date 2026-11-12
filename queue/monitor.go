package queue

import (
	"context"
	"time"

	"github.com/ryan053857/mygoravel/monitor"
)

// WithMonitor 记录的运行统计，label 都是 topic，值都是耗时。
const (
	// MetricSucceeded 是处理成功的任务和它们的处理耗时。
	MetricSucceeded = "queue.succeeded"
	// MetricRetried 是处理失败、等待重试的执行和它们的处理耗时。
	MetricRetried = "queue.retried"
	// MetricFailed 是转入失败任务表的任务和最后一次的处理耗时。
	MetricFailed = "queue.failed"
	// MetricWait 是任务从可以执行到开始处理等了多久。驱动给不出可以执行的时间时不记录，见 Job.AvailableAt。
	MetricWait = "queue.wait"
)

// WithMonitor 让 Worker 把每个 topic 的处理次数、耗时和等待时长记录到 m，供命令和后台页面查看，
// 用 Activity 查询。它是一个 Observer：所有驱动记录的数据相同。m 为 nil 时 panic。
func WithMonitor(m *monitor.Monitor) WorkerOption {
	if m == nil {
		panic("queue: nil monitor")
	}
	return WithObserver(func(_ context.Context, e Event) {
		topic := e.Job.Topic
		switch e.Kind {
		case JobStarted:
			if !e.Job.AvailableAt.IsZero() {
				m.RecordDuration(MetricWait, topic, max(e.Started.Sub(e.Job.AvailableAt), 0))
			}
		case JobSucceeded:
			m.RecordDuration(MetricSucceeded, topic, e.Duration)
		case JobRetrying:
			m.RecordDuration(MetricRetried, topic, e.Duration)
		case JobFailed:
			m.RecordDuration(MetricFailed, topic, e.Duration)
		}
	})
}

// TopicActivity 是一个 topic 在一段时间内的处理情况。
type TopicActivity struct {
	// Succeeded、Retried、Failed 是处理成功、失败后等待重试、转入失败任务表的执行，
	// 它们的 MeanDuration、MaxDuration 是处理耗时。
	Succeeded, Retried, Failed monitor.Aggregate
	// Wait 是任务从可以执行到开始处理的等待时长，驱动给不出时 Count 为 0。
	Wait monitor.Aggregate
}

// Executions 返回全部执行的汇总，用来计算总的平均和最长处理耗时。
func (a TopicActivity) Executions() monitor.Aggregate {
	return a.Succeeded.Add(a.Retried).Add(a.Failed)
}

// Activity 返回 WithMonitor 记录的每个 topic 在 from 到 to 之间的处理情况，键是 topic。
// 时间范围的含义见 monitor.Monitor.Totals。
func Activity(ctx context.Context, m *monitor.Monitor, from, to time.Time) (map[string]TopicActivity, error) {
	totals, err := m.Totals(ctx, from, to, MetricSucceeded, MetricRetried, MetricFailed, MetricWait)
	if err != nil {
		return nil, err
	}
	out := map[string]TopicActivity{}
	for _, t := range totals {
		a := out[t.Label]
		switch t.Metric {
		case MetricSucceeded:
			a.Succeeded = t.Aggregate
		case MetricRetried:
			a.Retried = t.Aggregate
		case MetricFailed:
			a.Failed = t.Aggregate
		case MetricWait:
			a.Wait = t.Aggregate
		}
		out[t.Label] = a
	}
	return out, nil
}
