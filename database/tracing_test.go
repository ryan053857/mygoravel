package database_test

import (
	"context"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ryan053857/mygoravel/database"
)

func TestChildSpans(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	tracer := database.ChildSpans(tp).Tracer("github.com/uptrace/bun")

	// 没有上级 span：不创建，ctx 原样返回。
	ctx := context.Background()
	got, span := tracer.Start(ctx, "SELECT")
	if span.IsRecording() || got != ctx {
		t.Fatal("root span created")
	}
	span.End()

	// 有上级 span：创建子 span。
	parentCtx, parent := tp.Tracer("app").Start(ctx, "GET /articles/{id}")
	_, child := tracer.Start(parentCtx, "SELECT")
	if !child.IsRecording() {
		t.Fatal("child span not recording")
	}
	child.End()
	parent.End()

	ended := rec.Ended()
	if len(ended) != 2 || ended[0].Name() != "SELECT" || ended[0].Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("spans %v", ended)
	}
}

func TestChildSpansNil(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("no panic")
		}
	}()
	database.ChildSpans(nil)
}
