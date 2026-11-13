package queue

import (
	"context"

	"github.com/go-tenon/tenon/logging"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// traceContext 按 W3C Trace Context 把链路写入任务元数据（traceparent、tracestate）。
var traceContext = propagation.TraceContext{}

// startSpan 还原任务元数据中的链路，设置了 WithTelemetry 时在其后开始本次执行的 span。
func (w *Worker) startSpan(ctx context.Context, j Job) context.Context {
	ctx = traceContext.Extract(ctx, propagation.MapCarrier(j.Metadata))
	if w.tracer == nil {
		return ctx
	}
	ctx, _ = w.tracer.Start(ctx, "process "+j.Topic, trace.WithSpanKind(trace.SpanKindConsumer), trace.WithAttributes(
		attrSystem.String(system),
		attribute.String("messaging.operation.type", "process"),
		attrDestination.String(j.Topic),
		attribute.String("messaging.message.id", j.ID.String()),
		attribute.Int("queue.job.attempt", j.Attempt),
	))
	return ctx
}

// endSpan 在任务结束的事件中结束本次执行的 span。
func (w *Worker) endSpan(ctx context.Context, e Event) {
	if w.tracer == nil || e.Kind == JobStarted {
		return
	}
	span := trace.SpanFromContext(ctx)
	span.SetAttributes(attrOutcome.String(e.Kind.String()))
	switch e.Kind {
	case JobRetrying, JobFailed:
		logging.RecordError(span, e.Err)
		span.SetStatus(codes.Error, e.Err.Error())
	}
	span.End()
}
