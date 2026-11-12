package schedule_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/codes"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
	"github.com/ryan053857/mygoravel/schedule"
)

func TestTracing(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	s := schedule.New(lock.New(locktest.NewStore()), schedule.DefaultConfig(), slog.New(slog.DiscardHandler),
		schedule.WithTelemetry(metricnoop.NewMeterProvider(), tp))
	var inTask trace.SpanContext
	s.Add("ok", "@everysecond", func(ctx context.Context) error {
		inTask = trace.SpanContextFromContext(ctx)
		return nil
	})
	s.Add("fails", "@everysecond", func(context.Context) error { return errors.New("bad") })
	if err := s.RunDue(context.Background(), base); err == nil {
		t.Fatal("expected the failing task's error")
	}
	byName := map[string]sdktrace.ReadOnlySpan{}
	for _, sp := range spans.Ended() {
		byName[sp.Name()] = sp
	}
	ok, fails := byName["schedule ok"], byName["schedule fails"]
	if ok == nil || fails == nil || len(byName) != 2 {
		t.Fatalf("spans %v", byName)
	}
	if ok.Parent().IsValid() || ok.SpanContext().SpanID() != inTask.SpanID() || ok.Status().Code == codes.Error {
		t.Fatalf("ok span parent %v status %v, task saw %v", ok.Parent(), ok.Status(), inTask)
	}
	if fails.Status().Code != codes.Error || ok.SpanContext().TraceID() == fails.SpanContext().TraceID() {
		t.Fatalf("fails span status %v", fails.Status())
	}
}
