package schedule

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-tenon/tenon/logging"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是调度器指标和链路的 instrumentation scope。
const scopeName = "github.com/go-tenon/tenon/schedule"

// 指标和 span 的属性名。
const (
	attrTask    = attribute.Key("schedule.task")
	attrOutcome = attribute.Key("schedule.task.outcome")
	attrReason  = attribute.Key("schedule.task.skip_reason")
)

// secondBuckets 是执行耗时的直方图分桶（秒），从 5 毫秒到 1 小时。
var secondBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300, 900, 3600}

// WithTelemetry 让调度器报告 OpenTelemetry 指标和链路。
//
// 指标的属性 schedule.task 是任务名：
//
//   - schedule.task.duration（直方图，秒）：执行耗时，属性 schedule.task.outcome 是 succeeded 或 failed
//     （返回错误或 panic）。按结果计数就能发现一段时间内没有成功执行过的任务。
//   - schedule.task.skipped（计数器）：本该在这个实例上执行却被跳过的次数，属性 schedule.task.skip_reason
//     是 overlap（上一次还没结束）或 lock_failed（锁存储出错）。由其他实例执行的触发不计入。
//
// 每次执行是一个 span，名称是 "schedule <任务名>"，是一条新链路的起点：任务中发布的队列任务、发出的请求都出现在这条链路中。
// 返回错误或 panic 时 span 标记为错误。被跳过或由其他实例执行的触发不创建 span。
//
// mp 或 tp 为 nil 时 panic；只需要其中一种时，另一个传 noop 实现。
func WithTelemetry(mp metric.MeterProvider, tp trace.TracerProvider) SchedulerOption {
	if mp == nil || tp == nil {
		panic("schedule: nil meter or tracer provider")
	}
	meter := mp.Meter(scopeName)
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
	record := func(ctx context.Context, e Event) {
		task := attrTask.String(e.Task)
		switch e.Kind {
		case TaskSucceeded, TaskFailed:
			duration.Record(ctx, e.Duration.Seconds(), metric.WithAttributes(task, attrOutcome.String(e.Kind.String())))
		case TaskSkipped:
			skipped.Add(ctx, 1, metric.WithAttributes(task, attrReason.String(string(e.SkipReason))))
		}
	}
	tracer := tp.Tracer(scopeName)
	return func(s *Scheduler) {
		s.tracer = tracer
		s.observers = append(s.observers, record)
	}
}

// startSpan 在设置了 WithTelemetry 时开始本次执行的 span，返回的函数以执行结果结束它。
func (s *Scheduler) startSpan(ctx context.Context, task string, tick time.Time) (context.Context, func(error)) {
	if s.tracer == nil {
		return ctx, func(error) {}
	}
	ctx, span := s.tracer.Start(ctx, "schedule "+task, trace.WithNewRoot(), trace.WithAttributes(
		attrTask.String(task), attribute.Int64("schedule.tick", tick.Unix()),
	))
	return ctx, func(err error) {
		if err != nil {
			logging.RecordError(span, err)
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
	}
}
