package httpx_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
)

func TestTracing(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	var logs bytes.Buffer
	logger := slog.New(logging.NewContextHandler(slog.NewJSONHandler(&logs, nil)))
	router := chi.NewRouter()
	router.Use(httpx.Tracing(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))))
	router.Use(httpx.Defaults(httpx.DefaultConfig(), logger, nil)...)
	router.Get("/articles/{id}", func(http.ResponseWriter, *http.Request) {})
	router.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	const parent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req := httptest.NewRequest(http.MethodGet, "/articles/1", nil)
	req.Header.Set("traceparent", parent)
	router.ServeHTTP(httptest.NewRecorder(), req)
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("%d spans", len(ended))
	}
	get, boom := ended[0], ended[1]
	if get.Name() != "GET /articles/{id}" || get.Parent().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		get.Parent().SpanID().String() != "00f067aa0ba902b7" || get.Status().Code == codes.Error {
		t.Fatalf("span %s parent %v status %v", get.Name(), get.Parent(), get.Status())
	}
	if boom.Name() != "GET /boom" || boom.Status().Code != codes.Error || boom.Parent().IsValid() {
		t.Fatalf("span %s parent %v status %v", boom.Name(), boom.Parent(), boom.Status())
	}

	// 访问日志带有请求的 trace_id 和 span_id。
	var first map[string]any
	if err := json.NewDecoder(&logs).Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first["trace_id"] != get.SpanContext().TraceID().String() || first["span_id"] != get.SpanContext().SpanID().String() {
		t.Fatalf("access log %v", first)
	}
}
