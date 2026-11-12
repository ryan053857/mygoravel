package queue

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// secondBuckets 是等待时长和处理耗时的直方图分桶（秒），从 5 毫秒到 1 小时。
var secondBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600}

// jobMetrics 是 Worker 记录的两个直方图。
type jobMetrics struct {
	wait, duration metric.Float64Histogram
}

func newJobMetrics(mp metric.MeterProvider) jobMetrics {
	meter := mp.Meter(scopeName)
	wait, err := meter.Float64Histogram("queue.job.wait", metric.WithUnit("s"),
		metric.WithDescription("Time a job waited between becoming available and starting."),
		metric.WithExplicitBucketBoundaries(secondBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.job.wait"))
	}
	duration, err := meter.Float64Histogram("queue.job.duration", metric.WithUnit("s"),
		metric.WithDescription("Time spent handling a job, by outcome."),
		metric.WithExplicitBucketBoundaries(secondBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.job.duration"))
	}
	return jobMetrics{wait: wait, duration: duration}
}

// relayRoute 是 queue.relay.jobs 的属性 queue.relay.route 的值：任务被转发的方式。
const (
	routePublished = "published" // 已经到时间，立即交给 NSQ
	routeDeferred  = "deferred"  // 作为 NSQ 的延迟消息
	routeScheduled = "scheduled" // 存入 queue_scheduled
)

// statusTimeout 是采集指标时读取队列状态的超时。
const statusTimeout = 5 * time.Second

// observeRelay 创建转发的指标：转发的任务数，以及采集时读取的复制槽积压、是否在转发、queue_scheduled 的任务数。
// 每个 Worker 实例都报告积压，只要还有一个实例活着就能看到；所有实例的 queue.relay.active 之和为 0 说明没有实例在转发。
func observeRelay(mp metric.MeterProvider, w *Worker) {
	meter := mp.Meter(scopeName)
	jobs, err := meter.Int64Counter("queue.relay.jobs", metric.WithUnit("{job}"),
		metric.WithDescription("Jobs relayed from the WAL, by route: published, deferred or scheduled."))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.relay.jobs"))
	}
	active, err := meter.Int64ObservableGauge("queue.relay.active",
		metric.WithDescription("1 when this worker holds the replication slot and relays jobs, 0 otherwise."))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.relay.active"))
	}
	lag, err := meter.Int64ObservableGauge("queue.relay.lag", metric.WithUnit("By"),
		metric.WithDescription("WAL retained by the replication slot that workers have not confirmed."))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.relay.lag"))
	}
	scheduled, err := meter.Int64ObservableGauge("queue.scheduled.jobs", metric.WithUnit("{job}"),
		metric.WithDescription("Jobs waiting in queue_scheduled until they are within max_defer."))
	if err != nil {
		panic(errors.Wrap(err, "queue: create queue.scheduled.jobs"))
	}
	_, err = meter.RegisterCallback(func(ctx context.Context, o metric.Observer) error {
		var a int64
		if w.relaying.Load() {
			a = 1
		}
		o.ObserveInt64(active, a)
		ctx, cancel := context.WithTimeout(ctx, statusTimeout)
		defer cancel()
		s, err := ReadStatus(ctx, w.db)
		if err != nil {
			return nil // 数据库不可用时不报告，告警看 queue.relay.active
		}
		if s.Exists {
			o.ObserveInt64(lag, s.Lag)
		}
		o.ObserveInt64(scheduled, int64(s.Scheduled))
		return nil
	}, active, lag, scheduled)
	if err != nil {
		panic(errors.Wrap(err, "queue: register relay metrics"))
	}
	w.relayJobs = jobs
}

// countRelayed 记录按 route 转发的任务数。
func (w *Worker) countRelayed(ctx context.Context, route string, n int) {
	if w.relayJobs != nil && n > 0 {
		w.relayJobs.Add(ctx, int64(n), metric.WithAttributes(attrRoute.String(route)))
	}
}

// record 是记录指标的 Observer。
func (m jobMetrics) record(ctx context.Context, e Event) {
	job := attribute.NewSet(attrSystem.String(system), attrDestination.String(e.Job.Topic))
	if e.Kind == JobStarted {
		if !e.Job.AvailableAt.IsZero() {
			m.wait.Record(ctx, max(time.Since(e.Job.AvailableAt), 0).Seconds(), metric.WithAttributeSet(job))
		}
		return
	}
	attrs := append(job.ToSlice(), attrOutcome.String(e.Kind.String()))
	m.duration.Record(ctx, e.Duration.Seconds(), metric.WithAttributes(attrs...))
}
