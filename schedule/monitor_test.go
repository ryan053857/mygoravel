package schedule_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/schedule"
)

func TestMonitor(t *testing.T) {
	ctx := context.Background()
	mon := monitor.New(dbtest.Open(t, monitor.Migrations()), nil)
	t.Cleanup(func() { _ = mon.Close(ctx) })

	locker := lock.New(locktest.NewStore())
	s := schedule.New(locker, schedule.DefaultConfig(), slog.New(slog.DiscardHandler), schedule.WithMonitor(mon))
	fail := true
	s.Add("reports", "@everysecond", func(context.Context) error {
		time.Sleep(10 * time.Millisecond)
		if fail {
			return errors.New("smtp down")
		}
		return nil
	})
	s.Add("busy", "@everysecond", func(context.Context) error { return nil })
	// 另一个实例正在执行 busy：本实例跳过。
	if _, err := locker.Acquire(ctx, "schedule:busy:running", time.Minute); err != nil {
		t.Fatal(err)
	}

	_ = s.RunDue(ctx, base)
	fail = false
	_ = s.RunDue(ctx, base.Add(time.Second))
	if err := mon.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	tasks, err := schedule.Activity(ctx, mon, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	reports, busy := tasks["reports"], tasks["busy"]
	if reports.Succeeded.Count != 1 || reports.Failed.Count != 1 || reports.Executions().MaxDuration() < 10*time.Millisecond ||
		busy.Skipped != 2 || busy.Succeeded.Count != 0 {
		t.Fatalf("activity %+v", tasks)
	}

	last, err := schedule.LastRuns(ctx, mon)
	if err != nil {
		t.Fatal(err)
	}
	r, ok := last["reports"]
	if len(last) != 1 || !ok || r.Failed() || !r.Tick.Equal(base.Add(time.Second)) || r.Started.IsZero() || r.Duration < 10*time.Millisecond {
		t.Fatalf("last runs %+v", last)
	}
}

// 失败的执行保存错误信息。
func TestMonitorLastFailure(t *testing.T) {
	ctx := context.Background()
	mon := monitor.New(dbtest.Open(t, monitor.Migrations()), nil)
	t.Cleanup(func() { _ = mon.Close(ctx) })
	s := schedule.New(lock.New(locktest.NewStore()), schedule.DefaultConfig(), slog.New(slog.DiscardHandler), schedule.WithMonitor(mon))
	s.Add("reports", "@everysecond", func(context.Context) error { return errors.New("smtp down") })
	_ = s.RunDue(ctx, base)
	if err := mon.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	last, err := schedule.LastRuns(ctx, mon)
	if r := last["reports"]; err != nil || !r.Failed() || r.Error != "smtp down" {
		t.Fatalf("last %+v err %v", last, err)
	}
}
