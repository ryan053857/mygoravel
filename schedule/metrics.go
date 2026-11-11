package schedule

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// meterName 是调度器指标的 instrumentation scope。
const meterName = "github.com/ryan053857/mygoravel/schedule"

// 指标的属性名。
const (
	attrTask    = attribute.Key("schedule.task")
	attrOutcome = attribute.Key("schedule.task.outcome")
	attrReason  = attribute.Key("schedule.task.skip_reason")
)

// 跳过执行的原因。
const (
	skipOverlap    = "overlap"     // 上一次还没结束
	skipLockFailed = "lock_failed" // 锁存储出错，无法确认由谁执行
)

// secondBuckets 是执行耗时的直方图分桶（秒），从 5 毫秒到 1 小时。
var secondBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600}

// SchedulerOption 调整 New 创建的调度器。
type SchedulerOption func(*Scheduler)

// WithMeterProvider 让调度器把每次执行记录为 OpenTelemetry 指标，属性 schedule.task 是任务名：
//
//   - schedule.task.duration（直方图，秒）：执行耗时，属性 schedule.task.outcome 是 succeeded 或 failed
//     （返回错误或 panic）。按结果计数就能发现一段时间内没有成功执行过的任务。
//   - schedule.task.skipped（计数器）：本该在这个实例上执行却被跳过的次数，属性 schedule.task.skip_reason
//     是 overlap（上一次还没结束）或 lock_failed（锁存储出错）。由其他实例执行的触发不计入。
//
// mp 为 nil 时 panic。
func WithMeterProvider(mp metric.MeterProvider) SchedulerOption {
	if mp == nil {
		panic("schedule: nil meter provider")
	}
	meter := mp.Meter(meterName)
	duration, err := meter.Float64Histogram("schedule.task.duration", metric.WithUnit("s"),
		metric.WithDescription("Time spent running a scheduled task, by outcome."),
		metric.WithExplicitBucketBoundaries(secondBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "schedule: create schedule.task.duration"))
	}
	skipped, err := meter.Int64Counter("schedule.task.skipped", metric.WithUnit("{run}"),
		metric.WithDescription("Scheduled runs skipped on this instance, by reason."))
	if err != nil {
		panic(errors.Wrap(err, "schedule: create schedule.task.skipped"))
	}
	return func(s *Scheduler) { s.metrics = &metrics{duration: duration, skipped: skipped} }
}

// WithTracerProvider 让调度器为每次执行创建一个 OpenTelemetry span，名称是 "schedule <任务名>"，
// 是一条新链路的起点：任务中发布的队列任务、发出的请求都出现在这条链路中。返回错误或 panic 时 span 标记为错误。
// 被跳过或由其他实例执行的触发不创建 span。tp 为 nil 时 panic。
func WithTracerProvider(tp trace.TracerProvider) SchedulerOption {
	if tp == nil {
		panic("schedule: nil tracer provider")
	}
	tracer := tp.Tracer(meterName)
	return func(s *Scheduler) { s.tracer = tracer }
}

// startSpan 在设置了 TracerProvider 时开始本次执行的 span，返回的函数以执行结果结束它。
func (s *Scheduler) startSpan(ctx context.Context, task string, tick time.Time) (context.Context, func(error)) {
	if s.tracer == nil {
		return ctx, func(error) {}
	}
	ctx, span := s.tracer.Start(ctx, "schedule "+task, trace.WithNewRoot(), trace.WithAttributes(
		attrTask.String(task), attribute.Int64("schedule.tick", tick.Unix()),
	))
	return ctx, func(err error) {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}

// metrics 是调度器的指标，未设置 MeterProvider 时为 nil。
type metrics struct {
	duration metric.Float64Histogram
	skipped  metric.Int64Counter
}

func (m *metrics) ran(ctx context.Context, task string, d time.Duration, err error) {
	if m == nil {
		return
	}
	outcome := "succeeded"
	if err != nil {
		outcome = "failed"
	}
	m.duration.Record(ctx, d.Seconds(), metric.WithAttributes(attrTask.String(task), attrOutcome.String(outcome)))
}

func (m *metrics) skip(ctx context.Context, task, reason string) {
	if m == nil {
		return
	}
	m.skipped.Add(ctx, 1, metric.WithAttributes(attrTask.String(task), attrReason.String(reason)))
}
