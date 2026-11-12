package queue_test

import (
	"context"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
)

// gauges 采集一次，返回 int64 仪表和计数（计数按 queue.relay.route 分开）的值。
func gauges(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch data := m.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, p := range data.DataPoints {
					got[m.Name] = p.Value
				}
			case metricdata.Sum[int64]:
				for _, p := range data.DataPoints {
					route, _ := p.Attributes.Value("queue.relay.route")
					got[m.Name+"/"+route.AsString()] = p.Value
				}
			}
		}
	}
	return got
}

func TestRelayMetrics(t *testing.T) {
	db := openDB(t)
	cfg := queuetest.Config(t)
	cfg.MaxDefer = time.Second
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	done := make(chan struct{}, 2)
	stop := start(t, db, cfg, func(w *queue.Worker) {
		jobs.Handle(w, func(context.Context, payload) error {
			done <- struct{}{}
			return nil
		})
	}, queue.WithTelemetry(mp, tracenoop.NewTracerProvider()))
	eventually(t, 5*time.Second, func() bool { return gauges(t, reader)["queue.relay.active"] == 1 })

	publish(t, db, 1)
	publish(t, db, 2, queue.Delay(200*time.Millisecond))
	publish(t, db, 3, queue.Delay(time.Hour))
	for range 2 {
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("jobs not handled")
		}
	}
	eventually(t, 5*time.Second, func() bool { return gauges(t, reader)["queue.relay.jobs/scheduled"] == 1 })
	got := gauges(t, reader)
	if got["queue.relay.jobs/published"] != 1 || got["queue.relay.jobs/deferred"] != 1 || got["queue.scheduled.jobs"] != 1 {
		t.Fatalf("metrics %v", got)
	}
	if _, ok := got["queue.relay.lag"]; !ok {
		t.Fatalf("no lag reported: %v", got)
	}

	stop()
	if got := gauges(t, reader); got["queue.relay.active"] != 0 {
		t.Fatalf("active after stop: %v", got)
	}
}
