package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/ryan053857/mygoravel/logging"
)

func TestContextHandlerAddsTraceIDs(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(logging.NewContextHandler(slog.NewJSONHandler(&buf, nil)))
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "op")
	defer span.End()

	decode := func() map[string]any {
		t.Helper()
		var m map[string]any
		if err := json.NewDecoder(&buf).Decode(&m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	logger.InfoContext(ctx, "in span")
	if m := decode(); m["trace_id"] != span.SpanContext().TraceID().String() || m["span_id"] != span.SpanContext().SpanID().String() {
		t.Fatalf("log %v", m)
	}
	logger.InfoContext(context.Background(), "no span")
	if m := decode(); m["trace_id"] != nil {
		t.Fatalf("log %v", m)
	}
	// 显式写入的 trace_id 优先，不重复输出。
	logger.InfoContext(logging.WithAttrs(ctx, slog.String("trace_id", "explicit")), "explicit")
	if raw := buf.String(); bytes.Count([]byte(raw), []byte(`"trace_id"`)) != 1 || decode()["trace_id"] != "explicit" {
		t.Fatalf("log %s", raw)
	}
}
