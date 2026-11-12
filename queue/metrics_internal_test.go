package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
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
	db := dbtesting.Open(t, sqlite.Driver, Migrations())
	failed := NewFailedJobs(db)
	for _, r := range []failedJob{
		{Driver: "fake", Topic: "full"}, {Driver: "fake", Topic: "gone"}, {Driver: "fake", Topic: "gone"},
		{Driver: "other", Topic: "full"}, // 其他驱动的失败任务不计入
	} {
		r.ID, r.Payload, r.FailedAt = uuid.New(), []byte("{}"), now
		if err := failed.record(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	reg := ObserveJobs(mp, d, failed)
	defer reg.Unregister()

	got := gauges(t, reader)
	for k, want := range map[string]float64{
		"queue.jobs full ready": 2, "queue.jobs full delayed": 1, "queue.jobs full retrying": 0, "queue.jobs full running": 3,
		"queue.jobs full failed": 1, "queue.jobs partial ready": 4, "queue.jobs partial running": 0, "queue.jobs partial failed": 0,
		// 只有失败任务的 topic：队列中没有它的任务。
		"queue.jobs gone failed": 2, "queue.jobs gone ready": 0, "queue.jobs gone delayed": 0, "queue.job.oldest_ready_age gone ": 0,
	} {
		if v, ok := got[k]; !ok || v != want {
			t.Errorf("%s = %v (reported %v), want %v", k, v, ok, want)
		}
	}
	if age := got["queue.job.oldest_ready_age full "]; age < 59 || age > 70 {
		t.Errorf("oldest ready age %v", age)
	}
	for _, k := range []string{"queue.jobs partial delayed", "queue.job.oldest_ready_age partial "} {
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
	if _, err := db.NewDelete().Model((*failedJob)(nil)).Where("1 = 1").Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	got = gauges(t, reader)
	for _, k := range []string{"queue.jobs full ready", "queue.jobs full running", "queue.jobs full failed", "queue.jobs partial ready",
		"queue.jobs gone failed", "queue.job.oldest_ready_age full ", "queue.job.oldest_ready_age partial "} {
		if v, ok := got[k]; !ok || v != 0 {
			t.Errorf("%s = %v (reported %v), want 0", k, v, ok)
		}
	}
	if _, ok := got["queue.jobs partial delayed"]; ok {
		t.Error("partial delayed must not be reported")
	}
}
