package database_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/go-tenon/tenon/acquire"
	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/errorx"
)

// openPool 打开一个最多 n 个连接、报告指标的连接池。
func openPool(t *testing.T, n int, opts ...database.Option) (*bun.DB, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	cfg := database.Config{DSN: dbtest.URL(t), MaxConns: n}
	opts = append(opts, database.WithTelemetry(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), tracenoop.NewTracerProvider()))
	db, err := database.Open(context.Background(), cfg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, reader
}

// hold 占住 db 的全部 n 个连接，直到返回的函数被调用。
func hold(t *testing.T, db *bun.DB, n int) (release func()) {
	t.Helper()
	ctx := context.Background()
	var txs []bun.Tx
	for range n {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, tx)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			for _, tx := range txs {
				_ = tx.Rollback()
			}
		})
	}
	t.Cleanup(release)
	return release
}

// 连接用满时，设置了 acquire 时限的语句在时限后返回 ErrBusy；没有设置的等到有连接为止。
func TestAcquireTimeout(t *testing.T) {
	t.Parallel()
	db, reader := openPool(t, 2)
	release := hold(t, db, 2)

	ctx := acquire.WithTimeout(context.Background(), 100*time.Millisecond)
	start := time.Now()
	_, err := db.NewRaw("SELECT 1").Exec(ctx)
	if !errors.Is(err, database.ErrBusy) || errorx.RetryAfterOf(err) != time.Second {
		t.Fatalf("err %v", err)
	}
	if d := time.Since(start); d < 100*time.Millisecond || d > time.Second {
		t.Fatalf("gave up after %v", d)
	}
	if _, err := db.BeginTx(ctx, nil); !errors.Is(err, database.ErrBusy) {
		t.Fatalf("begin: %v", err)
	}

	// 调用方自己的 ctx 先结束时，返回它的错误，不算 busy。
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := db.NewRaw("SELECT 1").Exec(short); errors.Is(err, database.ErrBusy) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("caller deadline: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := db.NewRaw("SELECT 1").Exec(context.Background())
		done <- err
	}()
	time.Sleep(200 * time.Millisecond)
	release()
	if err := <-done; err != nil {
		t.Fatalf("waiting query: %v", err)
	}

	// 有空闲连接时，时限不影响语句本身的执行。
	if _, err := db.NewRaw("SELECT pg_sleep(0.3)").Exec(ctx); err != nil {
		t.Fatalf("slow query under acquire timeout: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	timeouts := findMetric(t, rm, "db.client.connection.timeouts").Data.(metricdata.Sum[int64]).DataPoints
	if len(timeouts) != 1 || timeouts[0].Value != 2 {
		t.Fatalf("timeouts %+v", timeouts)
	}
	if v, _ := timeouts[0].Attributes.Value("db.client.connection.pool.name"); v.AsString() != "primary" {
		t.Fatalf("pool name %v", v)
	}
}

func TestPoolMetrics(t *testing.T) {
	t.Parallel()
	db, reader := openPool(t, 3, database.Name("analytics"))
	release := hold(t, db, 2)
	defer release()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, dp := range findMetric(t, rm, "db.client.connection.count").Data.(metricdata.Sum[int64]).DataPoints {
		state, _ := dp.Attributes.Value("db.client.connection.state")
		pool, _ := dp.Attributes.Value("db.client.connection.pool.name")
		if pool.AsString() != "analytics" {
			t.Fatalf("pool name %v", pool)
		}
		got[state.AsString()] = dp.Value
	}
	if got["used"] != 2 {
		t.Fatalf("count %v", got)
	}
	if max := findMetric(t, rm, "db.client.connection.max").Data.(metricdata.Sum[int64]).DataPoints; max[0].Value != 3 {
		t.Fatalf("max %+v", max)
	}
	if wait := findMetric(t, rm, "db.client.connection.wait_time").Data.(metricdata.Histogram[float64]).DataPoints; wait[0].Count < 2 {
		t.Fatalf("wait %+v", wait)
	}
}

// ReadOnly 的连接池拒绝写入，读取照常。
func TestReadOnly(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, nil)
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE notes (body text)"); err != nil {
		t.Fatal(err)
	}
	replica := dbtest.Replica(t, db)
	ctx := context.Background()
	if _, err := replica.ExecContext(ctx, "INSERT INTO notes VALUES ('x')"); err == nil || !database.IsReadOnly(err) {
		t.Fatalf("write on read-only pool: %v", err)
	}
	var n int
	if err := replica.NewRaw("SELECT count(*) FROM notes").Scan(ctx, &n); err != nil || n != 0 {
		t.Fatalf("read n=%d err=%v", n, err)
	}
}
