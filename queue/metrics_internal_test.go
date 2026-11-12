package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// statsDriver 是只实现 Stats 的驱动，返回 next 中的结果。
type statsDriver struct {
	Driver
	next  []TopicStats
	reads atomic.Int32
}

func (d *statsDriver) Name() string { return "fake" }

func (d *statsDriver) Stats(context.Context) ([]TopicStats, error) {
	d.reads.Add(1)
	return d.next, nil
}

// gauges 采集一次，返回 "指标名 topic state" 到数值的映射。
func gauges(t *testing.T, reader *sdkmetric.ManualReader) map[string]float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]float64{}
	key := func(name string, attrs attribute.Set) string {
		topic, _ := attrs.Value("messaging.destination.name")
		state, _ := attrs.Value("queue.job.state")
		return name + " " + topic.AsString() + " " + state.AsString()
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch g := m.Data.(type) {
			case metricdata.Gauge[int64]:
				for _, dp := range g.DataPoints {
					out[key(m.Name, dp.Attributes)] = float64(dp.Value)
				}
			case metricdata.Gauge[float64]:
				for _, dp := range g.DataPoints {
					out[key(m.Name, dp.Attributes)] = dp.Value
				}
			}
		}
	}
	return out
}

func TestObserveJobs(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	now := time.Now()
	d := &statsDriver{next: []TopicStats{
		{Topic: "full", Jobs: map[State]int{StateReady: 2, StateDelayed: 1, StateRetrying: 0, StateRunning: 3}, OldestReadyAt: now.Add(-time.Minute)},
		{Topic: "partial", Jobs: map[State]int{StateReady: 4, StateRunning: 0}},
	}}
	reg := ObserveJobs(mp, d, nil)
	defer reg.Unregister()

	got := gauges(t, reader)
	for k, want := range map[string]float64{
		"queue.jobs full ready": 2, "queue.jobs full delayed": 1, "queue.jobs full retrying": 0, "queue.jobs full running": 3,
		"queue.jobs partial ready": 4, "queue.jobs partial running": 0,
	} {
		if v, ok := got[k]; !ok || v != want {
			t.Errorf("%s = %v (reported %v), want %v", k, v, ok, want)
		}
	}
	if age := got["queue.job.oldest_ready_age full "]; age < 59 || age > 70 {
		t.Errorf("oldest ready age %v", age)
	}
	for _, k := range []string{"queue.jobs partial delayed", "queue.jobs full failed", "queue.job.oldest_ready_age partial "} {
		if _, ok := got[k]; ok {
			t.Errorf("%s must not be reported", k)
		}
	}

	// 缓存期内不再读取驱动。
	gauges(t, reader)
	if n := d.reads.Load(); n != 1 {
		t.Fatalf("driver read %d times", n)
	}

	// 任务清空后，出现过的 topic 报告 0，没有积压时积压时长为 0。
	defer func(old time.Duration) { statsMaxAge = old }(statsMaxAge)
	statsMaxAge = 0
	d.next = nil
	got = gauges(t, reader)
	for _, k := range []string{"queue.jobs full ready", "queue.jobs full running", "queue.jobs partial ready", "queue.job.oldest_ready_age full ", "queue.job.oldest_ready_age partial "} {
		if v, ok := got[k]; !ok || v != 0 {
			t.Errorf("%s = %v (reported %v), want 0", k, v, ok)
		}
	}
	if _, ok := got["queue.jobs partial delayed"]; ok {
		t.Error("partial delayed must not be reported")
	}
}
