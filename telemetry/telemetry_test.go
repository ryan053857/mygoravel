package telemetry_test

import (
	"compress/gzip"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	collectorlogs "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	"google.golang.org/protobuf/proto"

	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/telemetry"
)

// receiver 是记录每个路径收到的请求体和认证头的 OTLP/HTTP 接收端。
type receiver struct {
	mu     sync.Mutex
	bodies map[string][]byte
	auth   map[string]string
}

func (rc *receiver) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	zr, err := gzip.NewReader(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(zr)
	rc.mu.Lock()
	rc.bodies[r.URL.Path] = body
	rc.auth[r.URL.Path] = r.Header.Get("Authorization")
	rc.mu.Unlock()
	w.Header().Set("Content-Type", "application/x-protobuf")
}

func resourceAttrs(res *resourcepb.Resource) map[string]string {
	m := map[string]string{}
	for _, kv := range res.GetAttributes() {
		m[kv.GetKey()] = kv.GetValue().GetStringValue()
	}
	return m
}

func TestOpenPushesAllSignals(t *testing.T) {
	rc := &receiver{bodies: map[string][]byte{}, auth: map[string]string{}}
	srv := httptest.NewServer(rc)
	defer srv.Close()

	cfg := telemetry.DefaultConfig()
	cfg.Endpoint = srv.URL + "/api/default/"
	cfg.Headers = map[string]string{"Authorization": "Basic secret"}
	cfg.Service = "blog"
	cfg.Version = "v1.2.3"
	ctx := context.Background()
	tel, err := telemetry.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx, span := tel.Tracers.Tracer("test").Start(ctx, "publish")
	counter, err := tel.Meters.Meter("test").Int64Counter("articles.published")
	if err != nil {
		t.Fatal(err)
	}
	counter.Add(ctx, 1)
	logger := logging.New(logging.Config{Writer: io.Discard}, logging.Export(tel.Logs))
	logger.InfoContext(ctx, "published")
	span.End()
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()
	var traces collectortrace.ExportTraceServiceRequest
	var metrics collectormetrics.ExportMetricsServiceRequest
	var logs collectorlogs.ExportLogsServiceRequest
	signals := []struct {
		path     string
		msg      proto.Message
		resource func() *resourcepb.Resource
	}{
		{"/api/default/v1/traces", &traces, func() *resourcepb.Resource { return traces.GetResourceSpans()[0].GetResource() }},
		{"/api/default/v1/metrics", &metrics, func() *resourcepb.Resource { return metrics.GetResourceMetrics()[0].GetResource() }},
		{"/api/default/v1/logs", &logs, func() *resourcepb.Resource { return logs.GetResourceLogs()[0].GetResource() }},
	}
	for _, s := range signals {
		body, ok := rc.bodies[s.path]
		if !ok {
			t.Fatalf("nothing received at %s (got %v)", s.path, rc.bodies)
		}
		if rc.auth[s.path] != "Basic secret" {
			t.Errorf("%s: Authorization %q", s.path, rc.auth[s.path])
		}
		if err := proto.Unmarshal(body, s.msg); err != nil {
			t.Fatal(err)
		}
		attrs := resourceAttrs(s.resource())
		if attrs["service.name"] != "blog" || attrs["service.version"] != "v1.2.3" || attrs["service.instance.id"] == "" {
			t.Errorf("%s: resource %v", s.path, attrs)
		}
	}

	names := map[string]bool{}
	for _, sm := range metrics.GetResourceMetrics()[0].GetScopeMetrics() {
		for _, m := range sm.GetMetrics() {
			names[m.GetName()] = true
		}
	}
	if !names["articles.published"] || !names["go.goroutine.count"] {
		t.Errorf("metrics %v", names)
	}

	record := logs.GetResourceLogs()[0].GetScopeLogs()[0].GetLogRecords()[0]
	if record.GetBody().GetStringValue() != "published" || string(record.GetTraceId()) != string(traces.GetResourceSpans()[0].GetScopeSpans()[0].GetSpans()[0].GetTraceId()) {
		t.Errorf("log record %v not linked to the span", record)
	}
}

func TestOpenDisabled(t *testing.T) {
	tel, err := telemetry.Open(context.Background(), telemetry.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	_, span := tel.Tracers.Tracer("test").Start(context.Background(), "op")
	if span.IsRecording() {
		t.Error("span recorded without an endpoint")
	}
	// 导出的日志器不推送，基础输出照常。
	logger := logging.New(logging.Config{Writer: io.Discard}, logging.Export(tel.Logs))
	if !logger.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("base output disabled")
	}
	if err := tel.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestValidate(t *testing.T) {
	for name, cfg := range map[string]telemetry.Config{
		"relative endpoint": {Endpoint: "openobserve:5080", SampleRatio: 1},
		"other scheme":      {Endpoint: "grpc://openobserve:4317", SampleRatio: 1},
		"ratio above 1":     {SampleRatio: 1.5},
		"negative ratio":    {SampleRatio: -0.1},
	} {
		if err := cfg.Validate(); err == nil {
			t.Errorf("%s: no error", name)
		}
		if _, err := telemetry.Open(context.Background(), cfg); err == nil {
			t.Errorf("%s: Open accepted it", name)
		}
	}
	if err := (telemetry.Config{Endpoint: "https://api.example.com/otlp", SampleRatio: 0.1}).Validate(); err != nil {
		t.Error(err)
	}
}
