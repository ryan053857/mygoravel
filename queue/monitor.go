package queue

import (
	"context"
	"time"

	"github.com/ryan053857/mygoravel/monitor"
)

// WithMonitor 记录的运行统计，label 都是 topic。耗时以秒记录。
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

// WithMonitor 让 Worker 把每个 topic 的处理次数、耗时和等待时长记录到 rec，供命令和后台页面查看，
// 用 Activity 查询。它是一个 Observer：所有驱动记录的数据相同。rec 为 nil 时 panic。
func WithMonitor(rec *monitor.Recorder) WorkerOption {
	if rec == nil {
		panic("queue: nil recorder")
	}
	return WithObserver(func(_ context.Context, e Event) {
		topic := e.Job.Topic
		switch e.Kind {
		case JobStarted:
			if !e.Job.AvailableAt.IsZero() {
				rec.Record(MetricWait, topic, max(e.Started.Sub(e.Job.AvailableAt), 0).Seconds())
			}
		case JobSucceeded:
			rec.Record(MetricSucceeded, topic, e.Duration.Seconds())
		case JobRetrying:
			rec.Record(MetricRetried, topic, e.Duration.Seconds())
		case JobFailed:
			rec.Record(MetricFailed, topic, e.Duration.Seconds())
		}
	})
}

// TopicActivity 是一个 topic 在一段时间内的处理情况。
type TopicActivity struct {
	// Succeeded、Retried、Failed 是处理成功、失败后等待重试、转入失败任务表的执行次数。
	Succeeded, Retried, Failed int64
	// MeanDuration、MaxDuration 是这些执行的平均和最长处理耗时。
	MeanDuration, MaxDuration time.Duration
	// MeanWait、MaxWait 是任务从可以执行到开始处理的平均和最长等待时长，驱动给不出时为 0。
	MeanWait, MaxWait time.Duration
}

// Activity 返回 WithMonitor 记录的每个 topic 在 from 到 to 之间的处理情况，键是 topic。
// 时间范围的含义见 monitor.Store.Totals。
func Activity(ctx context.Context, stats *monitor.Store, from, to time.Time) (map[string]TopicActivity, error) {
	out := map[string]TopicActivity{}
	durations := map[string]monitor.Total{}
	for _, metric := range []string{MetricSucceeded, MetricRetried, MetricFailed, MetricWait} {
		totals, err := stats.Totals(ctx, metric, from, to)
		if err != nil {
			return nil, err
		}
		for _, t := range totals {
			a := out[t.Label]
			switch metric {
			case MetricSucceeded:
				a.Succeeded = t.Count
			case MetricRetried:
				a.Retried = t.Count
			case MetricFailed:
				a.Failed = t.Count
			case MetricWait:
				a.MeanWait, a.MaxWait = seconds(t.Mean()), seconds(t.Max)
			}
			if metric != MetricWait {
				d := durations[t.Label]
				d.Count, d.Sum, d.Max = d.Count+t.Count, d.Sum+t.Sum, max(d.Max, t.Max)
				durations[t.Label] = d
			}
			out[t.Label] = a
		}
	}
	for topic, d := range durations {
		a := out[topic]
		a.MeanDuration, a.MaxDuration = seconds(d.Mean()), seconds(d.Max)
		out[topic] = a
	}
	return out, nil
}

// seconds 把秒数转换为 time.Duration。
func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
