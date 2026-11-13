package logging_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/go-tenon/tenon/logging"
)

// recorder 是收集导出记录的 sdklog.Exporter。
type recorder struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (r *recorder) Export(_ context.Context, records []sdklog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range records {
		r.records = append(r.records, rec.Clone())
	}
	return nil
}
func (r *recorder) Shutdown(context.Context) error   { return nil }
func (r *recorder) ForceFlush(context.Context) error { return nil }

func exported(t *testing.T, cfg logging.Config, emit func(*slog.Logger)) (*bytes.Buffer, []sdklog.Record) {
	t.Helper()
	rec := &recorder{}
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(rec)))
	var buf bytes.Buffer
	cfg.Writer = &buf
	emit(logging.New(cfg, logging.Export(lp)))
	return &buf, rec.records
}

func attrs(r sdklog.Record) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		m[string(kv.Key)] = kv.Value
		return true
	})
	return m
}

func TestExport(t *testing.T) {
	cfg := logging.DefaultConfig()
	ctx, span := sdktrace.NewTracerProvider().Tracer("test").Start(context.Background(), "op")
	defer span.End()
	ctx = logging.WithAttrs(ctx, slog.String(logging.AttrRequestID, "req-1"))

	buf, records := exported(t, cfg, func(l *slog.Logger) {
		l.DebugContext(ctx, "hidden")
		l.With(logging.AttrComponent, "article").ErrorContext(ctx, "publish failed", "error", errors.New("boom"))
	})

	if !bytes.Contains(buf.Bytes(), []byte(`"trace_id"`)) {
		t.Fatalf("base output lost fields: %s", buf)
	}
	if len(records) != 1 {
		t.Fatalf("exported %d records, want 1 (debug filtered)", len(records))
	}
	r := records[0]
	if r.Body().AsString() != "publish failed" || r.Severity() != log.SeverityError {
		t.Fatalf("record %q severity %v", r.Body().AsString(), r.Severity())
	}
	if r.TraceID() != span.SpanContext().TraceID() || r.SpanID() != span.SpanContext().SpanID() {
		t.Fatalf("trace context %s/%s", r.TraceID(), r.SpanID())
	}
	got := attrs(r)
	for _, key := range []string{logging.AttrTraceID, logging.AttrSpanID} {
		if _, ok := got[key]; ok {
			t.Errorf("attribute %s duplicated: %v", key, got)
		}
	}
	if got[logging.AttrRequestID].AsString() != "req-1" || got[logging.AttrComponent].AsString() != "article" {
		t.Errorf("attributes %v", got)
	}
	if got["error"].Type() != attribute.MAP {
		t.Errorf("error not formatted: %v", got["error"])
	}
}

func TestExportKeepsGroupedKeys(t *testing.T) {
	_, records := exported(t, logging.DefaultConfig(), func(l *slog.Logger) {
		l.WithGroup("upstream").Info("call", logging.AttrTraceID, "abc")
	})
	if len(records) != 1 {
		t.Fatalf("exported %d records", len(records))
	}
	upstream := attrs(records[0])["upstream"]
	if kvs := upstream.AsMap(); len(kvs) != 1 || string(kvs[0].Key) != logging.AttrTraceID {
		t.Fatalf("group %v", upstream)
	}
}

func TestExportComponentLevels(t *testing.T) {
	cfg := logging.DefaultConfig()
	cfg.Levels = map[string]slog.Level{"billing": slog.LevelDebug}
	_, records := exported(t, cfg, func(l *slog.Logger) {
		l.Debug("dropped")
		l.With(logging.AttrComponent, "billing").Debug("kept")
	})
	if len(records) != 1 || records[0].Body().AsString() != "kept" {
		t.Fatalf("records %v", records)
	}
}
