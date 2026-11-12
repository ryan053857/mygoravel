package monitor_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
)

// base 是一个整点，测试的记录都在它之后的几分钟内。
var base = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

// open 返回 n 个共用一个数据库的 Monitor，相当于 n 个实例；它们的时钟停在 *at，测试结束时关闭。
func open(t *testing.T, driver database.Driver, at *time.Time, n int) []*monitor.Monitor {
	t.Helper()
	db := dbtesting.Open(t, driver, monitor.Migrations())
	ms := make([]*monitor.Monitor, n)
	for i := range ms {
		ms[i] = monitor.New(db, nil)
		monitor.SetNow(ms[i], func() time.Time { return *at })
		t.Cleanup(func() { _ = ms[i].Close(context.Background()) })
	}
	return ms
}

func flush(t *testing.T, ms ...*monitor.Monitor) {
	t.Helper()
	for _, m := range ms {
		if err := m.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func agg(count int64, sum, max float64) monitor.Aggregate {
	return monitor.Aggregate{Count: count, Sum: sum, Max: max}
}

func TestAggregates(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		at := base.Add(10 * time.Second)
		ms := open(t, driver, &at, 2) // 两个实例
		a, b := ms[0], ms[1]

		a.Record("mail.sent", "welcome", 0.5)
		a.Record("mail.sent", "welcome", 1.5)
		b.Record("mail.sent", "welcome", 3)
		a.Record("mail.sent", "reset", 2)
		a.Record("mail.failed", "welcome", 1)
		a.Record("other", "welcome", 1) // 没有查询的 metric
		flush(t, a, b)
		// 同一分钟再写一次，累加到已有的行上。
		a.Record("mail.sent", "welcome", 1)
		at = base.Add(2*time.Minute + 30*time.Second)
		a.Record("mail.sent", "welcome", 4)
		flush(t, a)

		totals, err := a.Totals(ctx, base, base.Add(time.Hour), "mail.sent", "mail.failed")
		if err != nil {
			t.Fatal(err)
		}
		want := []monitor.Total{
			{Metric: "mail.sent", Label: "reset", Aggregate: agg(1, 2, 2)},
			{Metric: "mail.failed", Label: "welcome", Aggregate: agg(1, 1, 1)},
			{Metric: "mail.sent", Label: "welcome", Aggregate: agg(5, 10, 4)},
		}
		if fmt.Sprint(totals) != fmt.Sprint(want) || totals[2].Mean() != 2 {
			t.Fatalf("totals %+v", totals)
		}
		// from 落在一分钟中间时包括这一分钟，to 不包括。
		totals, err = a.Totals(ctx, base.Add(30*time.Second), base.Add(2*time.Minute), "mail.sent")
		if err != nil || len(totals) != 2 || totals[1].Count != 4 {
			t.Fatalf("first minute totals %+v err %v", totals, err)
		}

		// 没有记录的分钟为零值。
		points, err := a.Series(ctx, "mail.sent", "welcome", base, base.Add(3*time.Minute), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		wantPoints := []monitor.Point{
			{At: base, Aggregate: agg(4, 6, 3)},
			{At: base.Add(time.Minute)},
			{At: base.Add(2 * time.Minute), Aggregate: agg(1, 4, 4)},
		}
		if fmt.Sprint(utc(points)) != fmt.Sprint(wantPoints) {
			t.Fatalf("points %+v", points)
		}
		hourly, err := a.Series(ctx, "mail.sent", "welcome", base.Add(30*time.Minute), base.Add(90*time.Minute), time.Hour)
		if err != nil || len(hourly) != 2 || !hourly[0].At.Equal(base) || hourly[0].Count != 0 || hourly[1].Count != 0 {
			t.Fatalf("hourly %+v err %v", hourly, err)
		}
		hourly, err = a.Series(ctx, "mail.sent", "welcome", base, base.Add(time.Hour), time.Hour)
		if err != nil || len(hourly) != 1 || hourly[0].Count != 5 || hourly[0].Max != 4 {
			t.Fatalf("hourly %+v err %v", hourly, err)
		}
	})
}

func utc(points []monitor.Point) []monitor.Point {
	out := make([]monitor.Point, len(points))
	for i, p := range points {
		p.At = p.At.UTC()
		out[i] = p
	}
	return out
}

func TestAggregateHelpers(t *testing.T) {
	ok := agg(2, 3, 2)
	failed := agg(1, 6, 6)
	if got := ok.Add(failed); got != agg(3, 9, 6) || got.MeanDuration() != 3*time.Second || got.MaxDuration() != 6*time.Second {
		t.Fatalf("add %+v", got)
	}
	// 与空的汇总相加时最大值不受 0 影响。
	negative := agg(1, -1, -1)
	if got := negative.Add(monitor.Aggregate{}); got != negative {
		t.Fatalf("add empty %+v", got)
	}
	if (monitor.Aggregate{}).Mean() != 0 {
		t.Fatal("mean of nothing")
	}
}

func TestRecordDuration(t *testing.T) {
	at := base
	m := open(t, sqlite.Driver, &at, 1)[0]
	m.RecordDuration("mail.sent", "welcome", 250*time.Millisecond)
	m.RecordDuration("mail.sent", "welcome", 750*time.Millisecond)
	flush(t, m)
	totals, err := m.Totals(context.Background(), base, base.Add(time.Minute), "mail.sent")
	if err != nil || len(totals) != 1 || totals[0].MeanDuration() != 500*time.Millisecond || totals[0].MaxDuration() != 750*time.Millisecond {
		t.Fatalf("totals %+v err %v", totals, err)
	}
}

// 超长的 label 来自运行时的数据，以摘要保存而不是 panic；查询时传原来的 label。
func TestLongLabel(t *testing.T) {
	ctx := context.Background()
	at := base
	m := open(t, sqlite.Driver, &at, 1)[0]
	long := strings.Repeat("t", monitor.MaxLabelLength+1)
	m.Record("queue.succeeded", long, 1)
	m.Set("schedule.last_run", long, 1)
	flush(t, m)
	totals, err := m.Totals(ctx, base, base.Add(time.Minute), "queue.succeeded")
	if err != nil || len(totals) != 1 || !strings.HasPrefix(totals[0].Label, "sha256:") || len(totals[0].Label) > monitor.MaxLabelLength {
		t.Fatalf("totals %+v err %v", totals, err)
	}
	if points, err := m.Series(ctx, "queue.succeeded", long, base, base.Add(time.Minute), time.Minute); err != nil || points[0].Count != 1 {
		t.Fatalf("series %+v err %v", points, err)
	}
	if values, err := m.Latest(ctx, "schedule.last_run"); err != nil || len(values) != 1 || values[0].Label != totals[0].Label {
		t.Fatalf("values %+v err %v", values, err)
	}
}

// 一次写入的行数超过一条 INSERT 的上限时分批写入。
func TestManyLabels(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		at := base
		m := open(t, driver, &at, 1)[0]
		for i := range 1200 {
			m.Record("cache.hit", fmt.Sprintf("cache-%04d", i), 1)
			m.Set("schedule.last_run", fmt.Sprintf("task-%04d", i), i)
		}
		flush(t, m)
		totals, err := m.Totals(context.Background(), base, base.Add(time.Minute), "cache.hit")
		if err != nil || len(totals) != 1200 {
			t.Fatalf("%d totals, err %v", len(totals), err)
		}
		values, err := m.Latest(context.Background(), "schedule.last_run")
		if err != nil || len(values) != 1200 {
			t.Fatalf("%d values, err %v", len(values), err)
		}
	})
}

type run struct {
	Status string `json:"status"`
}

func TestLatest(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		early, late := base, base.Add(time.Minute)
		db := dbtesting.Open(t, driver, monitor.Migrations())
		a, b := monitor.New(db, nil), monitor.New(db, nil)
		monitor.SetNow(a, func() time.Time { return early })
		monitor.SetNow(b, func() time.Time { return late })
		t.Cleanup(func() { _ = a.Close(ctx); _ = b.Close(ctx) })

		a.Set("schedule.last_run", "reports", run{"succeeded"})
		a.Set("schedule.last_run", "reports", run{"failed"}) // 同一个实例覆盖之前的值
		a.Set("schedule.last_run", "cleanup", run{"succeeded"})
		flush(t, a)
		// 后记录的值先写入，先记录的值后写入：保留后记录的。
		b.Set("schedule.last_run", "cleanup", run{"failed"})
		flush(t, b)
		a.Set("schedule.last_run", "cleanup", run{"skipped"})
		flush(t, a)

		values, err := a.Latest(ctx, "schedule.last_run")
		if err != nil || len(values) != 2 {
			t.Fatalf("values %+v err %v", values, err)
		}
		got := map[string]string{}
		for _, v := range values {
			var r run
			if err := v.Decode(&r); err != nil {
				t.Fatal(err)
			}
			got[v.Label] = r.Status + "@" + v.At.UTC().Format(time.TimeOnly)
		}
		if got["cleanup"] != "failed@09:01:00" || got["reports"] != "failed@09:00:00" || values[0].Label != "cleanup" {
			t.Fatalf("latest %v", got)
		}
	})
}

func TestCleanup(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		at := time.Now().Add(-3 * time.Hour)
		m := open(t, driver, &at, 1)[0]
		for range 3 {
			m.Record("mail.sent", "welcome", 1)
			m.Set("schedule.last_run", "reports", 1)
			at = at.Add(time.Minute)
		}
		at = time.Now()
		m.Record("mail.sent", "welcome", 1)
		flush(t, m)

		cleanup := m.Cleanup(time.Hour)
		if _, err := cleanup(ctx, 0); err == nil {
			t.Fatal("expected an error for limit 0")
		}
		if n, err := cleanup(ctx, 2); err != nil || n != 2 {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := cleanup(ctx, 10); err != nil || n != 1 {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		totals, err := m.Totals(ctx, time.Now().Add(-24*time.Hour), time.Now().Add(time.Minute), "mail.sent")
		if err != nil || len(totals) != 1 || totals[0].Count != 1 {
			t.Fatalf("remaining %+v err %v", totals, err)
		}
		// 最新值不清理。
		if values, err := m.Latest(ctx, "schedule.last_run"); err != nil || len(values) != 1 {
			t.Fatalf("values %+v err %v", values, err)
		}
	})
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	m := monitor.New(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()), nil)
	m.Record("mail.sent", "welcome", 1)
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	m.Record("mail.sent", "welcome", 1) // 关闭之后被丢弃
	if err := m.Close(ctx); err != nil {
		t.Fatal(err)
	}
	// 关闭之后查询照常可用。
	totals, err := m.Totals(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), "mail.sent")
	if err != nil || len(totals) != 1 || totals[0].Count != 1 {
		t.Fatalf("totals %+v err %v", totals, err)
	}
}

// 定期写入，并发的记录和写入不会丢失或重复计数。
func TestConcurrentRecords(t *testing.T) {
	defer monitor.SetFlushInterval(5 * time.Millisecond)()
	ctx := context.Background()
	m := monitor.New(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()), nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				m.Record("cache.hit", "articles", 1)
			}
		})
	}
	wg.Wait()
	total := func() int64 {
		totals, err := m.Totals(ctx, time.Now().Add(-time.Hour), time.Now().Add(time.Minute), "cache.hit")
		if err != nil {
			t.Fatal(err)
		}
		if len(totals) == 0 {
			return 0
		}
		return totals[0].Count
	}
	// 定期写入，不需要 Flush 或 Close。
	deadline := time.Now().Add(5 * time.Second)
	for total() != 4000 {
		if time.Now().After(deadline) {
			t.Fatalf("total %d, want 4000", total())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := m.Close(ctx); err != nil || total() != 4000 {
		t.Fatalf("after close total %d err %v", total(), err)
	}
}

func TestInvalidArguments(t *testing.T) {
	at := base
	m := open(t, sqlite.Driver, &at, 1)[0]
	ctx := context.Background()
	for name, f := range map[string]func(){
		"empty metric": func() { m.Record("", "x", 1) },
		"long metric":  func() { m.Set(strings.Repeat("m", monitor.MaxMetricLength+1), "x", 1) },
		"no metrics":   func() { _, _ = m.Totals(ctx, base, base) },
		"step":         func() { _, _ = m.Series(ctx, "m", "x", base, base, 90*time.Second) },
		"keep":         func() { m.Cleanup(0) },
		"nil database": func() { monitor.New(nil, nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected a panic", name)
				}
			}()
			f()
		}()
	}
}
