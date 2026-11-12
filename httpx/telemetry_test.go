package httpx_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
)

func TestTelemetryMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	router := chi.NewRouter()
	router.Use(httpx.Telemetry(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), tracenoop.NewTracerProvider()))
	router.Use(httpx.Defaults(httpx.DefaultConfig(), slog.New(slog.DiscardHandler), nil)...)
	router.Get("/articles/{id}", func(http.ResponseWriter, *http.Request) {})
	router.Route("/api", func(r chi.Router) {
		r.Post("/panic", func(http.ResponseWriter, *http.Request) { panic("boom") })
	})
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/articles/1", nil),
		httptest.NewRequest(http.MethodGet, "/articles/2", nil),
		httptest.NewRequest(http.MethodPost, "/api/panic", nil),
		httptest.NewRequest(http.MethodGet, "/no/such/path", nil),
		httptest.NewRequest("PURGE", "/articles/1", nil),
	} {
		router.ServeHTTP(httptest.NewRecorder(), req)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	h := rm.ScopeMetrics[0].Metrics[0]
	if h.Name != "http.server.request.duration" || h.Unit != "s" {
		t.Fatalf("metric %s %s", h.Name, h.Unit)
	}
	counts := map[attribute.Distinct]uint64{}
	for _, dp := range h.Data.(metricdata.Histogram[float64]).DataPoints {
		counts[dp.Attributes.Equivalent()] = dp.Count
	}
	set := func(kv ...attribute.KeyValue) attribute.Distinct {
		s := attribute.NewSet(append(kv, attribute.String("url.scheme", "http"))...)
		return s.Equivalent()
	}
	want := map[attribute.Distinct]uint64{
		set(attribute.String("http.request.method", "GET"), attribute.Int("http.response.status_code", 200),
			attribute.String("http.route", "/articles/{id}")): 2,
		set(attribute.String("http.request.method", "POST"), attribute.Int("http.response.status_code", 500),
			attribute.String("http.route", "/api/panic"), attribute.String("error.type", "500")): 1,
		set(attribute.String("http.request.method", "GET"), attribute.Int("http.response.status_code", 404)):    1,
		set(attribute.String("http.request.method", "_OTHER"), attribute.Int("http.response.status_code", 405)): 1,
	}
	if len(counts) != len(want) {
		t.Errorf("got %d series, want %d", len(counts), len(want))
	}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("series %v: count %d, want %d", k, counts[k], n)
		}
	}
	if t.Failed() {
		for _, dp := range h.Data.(metricdata.Histogram[float64]).DataPoints {
			t.Logf("%v %d", dp.Attributes.ToSlice(), dp.Count)
		}
	}
}

func TestTelemetryTracing(t *testing.T) {
	spans := tracetest.NewSpanRecorder()
	var logs bytes.Buffer
	logger := slog.New(logging.NewContextHandler(slog.NewJSONHandler(&logs, nil)))
	router := chi.NewRouter()
	router.Use(httpx.Telemetry(metricnoop.NewMeterProvider(), sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))))
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

func TestTelemetryLinksMetricsToSpan(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	spans := tracetest.NewSpanRecorder()
	router := chi.NewRouter()
	router.Use(httpx.Telemetry(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))))
	router.Get("/articles/{id}", func(http.ResponseWriter, *http.Request) {})
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/articles/1", nil))

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	dp := rm.ScopeMetrics[0].Metrics[0].Data.(metricdata.Histogram[float64]).DataPoints[0]
	traceID := spans.Ended()[0].SpanContext().TraceID()
	if len(dp.Exemplars) != 1 || !bytes.Equal(dp.Exemplars[0].TraceID, traceID[:]) {
		t.Fatalf("exemplars %v, trace %s", dp.Exemplars, traceID)
	}
}
