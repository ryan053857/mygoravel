package httpx_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/ryan053857/mygoravel/httpx"
)

func TestMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	router := chi.NewRouter()
	router.Use(httpx.Metrics(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))))
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
