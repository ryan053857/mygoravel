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
