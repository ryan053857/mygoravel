package queue

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// traceContext 按 W3C Trace Context 把链路写入任务元数据（traceparent、tracestate）。
var traceContext = propagation.TraceContext{}

// WithTracerProvider 让订阅者为每次执行创建一个 OpenTelemetry consumer span，名称是 "process <topic>"，
// 接在发布任务时的链路之后：HTTP 请求中发布的任务，执行时出现在同一条链路中。
// 处理失败、重试或租约丢失时 span 标记为错误。
//
// 不设置时，任务仍然带着发布方的链路：处理函数的 ctx 中是发布方的 span，处理函数自己创建的 span 接在它之后。
// tp 为 nil 时 panic。
func WithTracerProvider(tp trace.TracerProvider) SubscriberOption {
	if tp == nil {
		panic("queue: nil tracer provider")
	}
	tracer := tp.Tracer(meterName)
	return func(s *Subscriber) { s.tracer = tracer }
}

// startSpan 还原任务元数据中的链路，设置了 TracerProvider 时在其后开始本次执行的 span。
func (s *Subscriber) startSpan(ctx context.Context, j Job) context.Context {
	ctx = traceContext.Extract(ctx, propagation.MapCarrier(j.Metadata))
	if s.tracer == nil {
		return ctx
	}
	ctx, _ = s.tracer.Start(ctx, "process "+j.Topic, trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attribute.String("messaging.system", s.driver.Name()),
		attribute.String("messaging.operation.type", "process"),
		attribute.String("messaging.destination.name", j.Topic),
		attribute.String("messaging.message.id", j.ID.String()),
		attribute.Int("queue.job.attempt", j.Attempt),
	))
	return ctx
}

// endSpan 在任务结束的事件中结束本次执行的 span。
func (s *Subscriber) endSpan(ctx context.Context, e Event) {
	if s.tracer == nil || e.Kind == JobStarted {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attrOutcome.String(e.Kind.String()))
	switch e.Kind {
	case JobRetrying, JobFailed:
		span.RecordError(e.Err)
		span.SetStatus(codes.Error, e.Err.Error())
	case JobLeaseLost:
		span.SetStatus(codes.Error, "lease lost")
	}
	span.End()
}
