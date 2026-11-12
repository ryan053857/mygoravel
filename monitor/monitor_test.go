package monitor_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
)

// base 是一个整点，测试的记录都在它之后的几分钟内。
var base = time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)

func open(t *testing.T, driver database.Driver) (*bun.DB, *monitor.Store) {
	t.Helper()
	db := dbtesting.Open(t, driver, monitor.Migrations())
	return db, monitor.NewStore(db)
}

// recorder 返回一个时钟停在 *at 的 Recorder，测试结束时关闭。
func recorder(t *testing.T, store *monitor.Store, at *time.Time) *monitor.Recorder {
	t.Helper()
	r := monitor.NewRecorder(store, nil)
	monitor.SetNow(r, func() time.Time { return *at })
	t.Cleanup(func() { _ = r.Close(context.Background()) })
	return r
}

func flush(t *testing.T, rs ...*monitor.Recorder) {
	t.Helper()
	for _, r := range rs {
		if err := r.Flush(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAggregates(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		_, store := open(t, driver)
		at := base.Add(10 * time.Second)
		a, b := recorder(t, store, &at), recorder(t, store, &at) // 两个实例

		a.Record("queue.processed", "mail", 0.5)
		a.Record("queue.processed", "mail", 1.5)
		b.Record("queue.processed", "mail", 3)
		a.Record("queue.processed", "report", 2)
		a.Record("queue.failed", "mail", 1) // 其他 metric
		flush(t, a, b)
		// 同一分钟再写一次，累加到已有的行上。
		a.Record("queue.processed", "mail", 1)
		at = base.Add(2*time.Minute + 30*time.Second)
		a.Record("queue.processed", "mail", 4)
		flush(t, a)

		totals, err := store.Totals(ctx, "queue.processed", base, base.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		want := []monitor.Total{{Label: "mail", Count: 5, Sum: 10, Max: 4}, {Label: "report", Count: 1, Sum: 2, Max: 2}}
		if fmt.Sprint(totals) != fmt.Sprint(want) || totals[0].Mean() != 2 {
			t.Fatalf("totals %+v", totals)
		}
		// from 落在一分钟中间时包括这一分钟，to 不包括。
		if totals, _ := store.Totals(ctx, "queue.processed", base.Add(30*time.Second), base.Add(2*time.Minute)); len(totals) != 2 || totals[0].Count != 4 {
			t.Fatalf("first minute totals %+v", totals)
		}

		points, err := store.Series(ctx, "queue.processed", "mail", base, base.Add(time.Hour), time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		wantPoints := []monitor.Point{
			{At: base, Count: 4, Sum: 6, Max: 3},
			{At: base.Add(2 * time.Minute), Count: 1, Sum: 4, Max: 4},
		}
		if len(points) != 2 || fmt.Sprint(pointsUTC(points)) != fmt.Sprint(wantPoints) {
			t.Fatalf("points %+v", points)
		}
		hourly, err := store.Series(ctx, "queue.processed", "mail", base, base.Add(time.Hour), time.Hour)
		if err != nil || len(hourly) != 1 || !hourly[0].At.Equal(base) || hourly[0].Count != 5 || hourly[0].Max != 4 {
			t.Fatalf("hourly %+v err %v", hourly, err)
		}
	})
}

func pointsUTC(points []monitor.Point) []monitor.Point {
	out := make([]monitor.Point, len(points))
	for i, p := range points {
		p.At = p.At.UTC()
		out[i] = p
	}
	return out
}

// 一次写入的行数超过一条 INSERT 的上限时分批写入。
func TestManyLabels(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		_, store := open(t, driver)
		at := base
		r := recorder(t, store, &at)
		for i := range 1200 {
			r.Record("cache.hit", fmt.Sprintf("cache-%04d", i), 1)
			r.Set("schedule.last_run", fmt.Sprintf("task-%04d", i), i)
		}
		flush(t, r)
		totals, err := store.Totals(context.Background(), "cache.hit", base, base.Add(time.Minute))
		if err != nil || len(totals) != 1200 {
			t.Fatalf("%d totals, err %v", len(totals), err)
		}
		values, err := store.Latest(context.Background(), "schedule.last_run")
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
		_, store := open(t, driver)
		early, late := base, base.Add(time.Minute)
		a, b := recorder(t, store, &early), recorder(t, store, &late)

		a.Set("schedule.last_run", "reports", run{"succeeded"})
		a.Set("schedule.last_run", "reports", run{"failed"}) // 同一个实例覆盖之前的值
		a.Set("schedule.last_run", "cleanup", run{"succeeded"})
		flush(t, a)
		// 后记录的值先写入，先记录的值后写入：保留后记录的。
		b.Set("schedule.last_run", "cleanup", run{"failed"})
		flush(t, b)
		a.Set("schedule.last_run", "cleanup", run{"skipped"})
		flush(t, a)

		values, err := store.Latest(ctx, "schedule.last_run")
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
		_, store := open(t, driver)
		at := time.Now().Add(-3 * time.Hour)
		r := recorder(t, store, &at)
		for range 3 {
			r.Record("queue.processed", "mail", 1)
			r.Set("schedule.last_run", "reports", 1)
			at = at.Add(time.Minute)
		}
		at = time.Now()
		r.Record("queue.processed", "mail", 1)
		flush(t, r)

		cleanup := store.Cleanup(time.Hour)
		if _, err := cleanup(ctx, 0); err == nil {
			t.Fatal("expected an error for limit 0")
		}
		if n, err := cleanup(ctx, 2); err != nil || n != 2 {
			t.Fatalf("first cleanup n=%d err=%v", n, err)
		}
		if n, err := cleanup(ctx, 10); err != nil || n != 1 {
			t.Fatalf("second cleanup n=%d err=%v", n, err)
		}
		totals, err := store.Totals(ctx, "queue.processed", time.Now().Add(-24*time.Hour), time.Now().Add(time.Minute))
		if err != nil || len(totals) != 1 || totals[0].Count != 1 {
			t.Fatalf("remaining %+v err %v", totals, err)
		}
		// 最新值不清理。
		if values, err := store.Latest(ctx, "schedule.last_run"); err != nil || len(values) != 1 {
			t.Fatalf("values %+v err %v", values, err)
		}
	})
}

func TestClose(t *testing.T) {
	ctx := context.Background()
	_, store := open(t, sqlite.Driver)
	r := monitor.NewRecorder(store, nil)
	r.Record("queue.processed", "mail", 1)
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	r.Record("queue.processed", "mail", 1) // 关闭之后被丢弃
	if err := r.Close(ctx); err != nil {
		t.Fatal(err)
	}
	totals, err := store.Totals(ctx, "queue.processed", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
	if err != nil || len(totals) != 1 || totals[0].Count != 1 {
		t.Fatalf("totals %+v err %v", totals, err)
	}
}

func TestInvalidArguments(t *testing.T) {
	_, store := open(t, sqlite.Driver)
	at := base
	r := recorder(t, store, &at)
	for name, f := range map[string]func(){
		"empty metric": func() { r.Record("", "x", 1) },
		"long metric":  func() { r.Set(string(make([]byte, monitor.MaxMetricLength+1)), "x", 1) },
		"long label":   func() { r.Record("m", string(make([]byte, monitor.MaxLabelLength+1)), 1) },
		"step":         func() { _, _ = store.Series(context.Background(), "m", "x", base, base, 90*time.Second) },
		"keep":         func() { store.Cleanup(0) },
		"nil store":    func() { monitor.NewRecorder(nil, nil) },
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

// Recorder 定期写入，并发的记录和写入不会丢失或重复计数。
func TestConcurrentRecords(t *testing.T) {
	defer monitor.SetFlushInterval(5 * time.Millisecond)()
	ctx := context.Background()
	_, store := open(t, sqlite.Driver)
	r := monitor.NewRecorder(store, nil)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 500 {
				r.Record("cache.hit", "articles", 1)
			}
		})
	}
	wg.Wait()
	total := func() int64 {
		totals, err := store.Totals(ctx, "cache.hit", time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
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
	if err := r.Close(ctx); err != nil || total() != 4000 {
		t.Fatalf("after close total %d err %v", total(), err)
	}
}
