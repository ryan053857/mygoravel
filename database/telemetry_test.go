package database_test

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/logging/logtest"
)

type pgClass struct {
	Name string `bun:"relname"`
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[kv.Key] = kv.Value
	}
	return m
}

func TestTelemetry(t *testing.T) {
	ctx := context.Background()
	reader := sdkmetric.NewManualReader()
	spans := tracetest.NewSpanRecorder()
	db, err := database.Open(ctx, database.Config{DSN: dbtest.URL(t)}, database.WithTelemetry(
		sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)),
		sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// 没有上级 span 的查询只计入指标。
	if _, err := db.NewRaw("SELECT 1").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(spans.Ended()); n != 0 {
		t.Fatalf("%d root spans", n)
	}
	ctx, parent := sdktrace.NewTracerProvider().Tracer("test").Start(ctx, "request")
	defer parent.End()

	var row pgClass
	if err := db.NewSelect().Table("pg_class").Column("relname").Where("relname = ?", "pg_class").Scan(ctx, &row); err != nil {
		t.Fatal(err)
	}
	err = db.NewSelect().Table("pg_class").Column("relname").Where("relname = ?", "no such table").Scan(ctx, &row)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := db.NewRaw("SELECT 1 / ?", 0).Exec(ctx); err == nil {
		t.Fatal("division by zero succeeded")
	}

	ended := spans.Ended()
	if len(ended) != 3 {
		t.Fatalf("%d spans", len(ended))
	}
	found, missing, failed := ended[0], ended[1], ended[2]
	a := spanAttrs(found)
	if found.Name() != "SELECT pg_class" || a["db.system.name"].AsString() != "postgresql" ||
		a["db.operation.name"].AsString() != "SELECT" || a["db.collection.name"].AsString() != "pg_class" ||
		a["db.namespace"].AsString() != "postgres" || a["server.address"].AsString() != "127.0.0.1" ||
		found.Status().Code != 0 {
		t.Fatalf("select span %s %v %v", found.Name(), a, found.Status())
	}
	// 参数的值不会出现在链路中。
	if text := a["db.query.text"].AsString(); strings.Contains(text, "pg_class'") || !strings.Contains(text, "relname = ?") {
		t.Fatalf("query text %q", text)
	}
	if _, ok := spanAttrs(missing)["error.type"]; ok || missing.Status().Code != 0 {
		t.Fatalf("no rows is not an error: %v", spanAttrs(missing))
	}
	fa := spanAttrs(failed)
	if failed.Name() != "SELECT" || fa["error.type"].AsString() != "22012" || fa["db.response.status_code"].AsString() != "22012" ||
		failed.Status().Description == "" || fa["db.query.text"].AsString() != "SELECT ? / ?" {
		t.Fatalf("failed span %s %v %v", failed.Name(), fa, failed.Status())
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatal(err)
	}
	m := rm.ScopeMetrics[0].Metrics[0]
	if m.Name != "db.client.operation.duration" || m.Unit != "s" {
		t.Fatalf("metric %s %s", m.Name, m.Unit)
	}
	var selects, errs uint64
	for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
		if v, ok := dp.Attributes.Value("db.collection.name"); ok && v.AsString() == "pg_class" {
			selects += dp.Count
		}
		if v, ok := dp.Attributes.Value("error.type"); ok && v.AsString() == "22012" {
			errs += dp.Count
		}
		if _, ok := dp.Attributes.Value("db.query.text"); ok {
			t.Fatal("query text in metric attributes")
		}
	}
	if selects != 2 || errs != 1 {
		t.Fatalf("selects %d, errors %d", selects, errs)
	}
}

func TestTransactionSpans(t *testing.T) {
	ctx := context.Background()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	db, err := database.Open(ctx, database.Config{DSN: dbtest.URL(t)}, database.WithTelemetry(metricnoop.NewMeterProvider(), tp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ctx, parent := tp.Tracer("test").Start(ctx, "request")
	err = database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		_, err := tx.NewRaw("SELECT 1").Exec(ctx)
		return err
	})
	parent.End()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range spans.Ended() {
		if s.Parent().SpanID() == parent.SpanContext().SpanID() {
			names = append(names, s.Name())
		}
	}
	if strings.Join(names, ",") != "BEGIN,SELECT,COMMIT" {
		t.Fatalf("spans under the request: %v", names)
	}
}

func TestSlowQueryLog(t *testing.T) {
	ctx := context.Background()
	rec := logtest.New()
	cfg := database.Config{DSN: dbtest.URL(t), SlowQuery: 20 * time.Millisecond}
	db, err := database.Open(ctx, cfg, database.WithLogger(rec.Logger()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.NewRaw("SELECT 1").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.NewRaw("SELECT pg_sleep(?), ?", 0.05, "secret").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	var slow []logtest.Record
	for _, r := range rec.Records() {
		if r.Message == "slow query" {
			slow = append(slow, r)
		}
	}
	if len(slow) != 1 {
		t.Fatalf("slow queries %v", rec.Records())
	}
	r := slow[0]
	if r.Level != slog.LevelWarn || r.Attr("component") != "database" || r.Attr("query") != "SELECT pg_sleep(?), ?" ||
		r.Attr("db.operation.name") != "SELECT" || r.Attr("duration").(time.Duration) < 50*time.Millisecond {
		t.Fatalf("record %+v", r)
	}

	// 不记录慢查询、也不报告指标链路时不安装钩子。
	cfg.SlowQuery = 0
	plain, err := database.Open(ctx, cfg, database.WithLogger(rec.Logger()))
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	if _, err := plain.NewRaw("SELECT pg_sleep(0.03)").Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if n := len(rec.Records()); n != len(slow) {
		t.Fatalf("logged without threshold: %v", rec.Records())
	}
}

func TestOpenOptions(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil provider accepted")
		}
	}()
	database.WithTelemetry(nil, tracenoop.NewTracerProvider())
}
