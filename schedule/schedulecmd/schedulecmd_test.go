package schedulecmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/schedule"
	"github.com/ryan053857/mygoravel/schedule/schedulecmd"
)

func TestSchedules(t *testing.T) {
	ctx := context.Background()
	locker := lock.New(locktest.NewStore())
	noop := func(context.Context) error { return nil }
	calls := 0
	tasks := func() *schedule.Scheduler {
		calls++
		s := schedule.New(locker, schedule.DefaultConfig(), nil)
		s.Add("reports:daily", "@daily", noop, schedule.Describe("生成前一天的报表"))
		s.Add("cache:warm", "@every 30s", noop, schedule.EveryInstance())
		return s
	}
	cmd := schedulecmd.New(tasks, nil)
	if calls != 0 {
		t.Fatal("tasks called while declaring the command")
	}
	// 另一个实例正在执行 reports:daily。
	if _, err := locker.Acquire(ctx, "schedule:reports:daily:running", time.Minute); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for line := range strings.Lines(out.String()) {
		rows = append(rows, strings.Fields(line))
	}
	if len(rows) != 3 || strings.Join(rows[0], " ") != "TASK EXPRESSION NEXT RUN IN RUNNING DESCRIPTION" {
		t.Fatalf("rows %q", rows)
	}
	// 名称、表达式、日期、时间、距现在多久、是否在执行、说明。
	daily, warm := rows[1], rows[2]
	if daily[0] != "reports:daily" || daily[1] != "@daily" || daily[3] != "00:00:00" || daily[5] != "yes" || daily[6] != "生成前一天的报表" {
		t.Fatalf("daily %q", daily)
	}
	if in, err := time.ParseDuration(warm[5]); warm[0] != "cache:warm" || warm[1] != "@every" || err != nil || in > 30*time.Second || warm[6] != "-" {
		t.Fatalf("warm %q", warm)
	}
}

func TestSchedulesLastRun(t *testing.T) {
	ctx := context.Background()
	mon := monitor.New(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()), nil)
	t.Cleanup(func() { _ = mon.Close(ctx) })
	started := time.Date(2026, 11, 12, 3, 0, 0, 0, time.Local)
	mon.Set(schedule.MetricLastRun, "reports:daily", schedule.Run{Tick: started, Started: started, Duration: 1500 * time.Millisecond, Error: "smtp down"})
	if err := mon.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	noop := func(context.Context) error { return nil }
	cmd := schedulecmd.New(func() *schedule.Scheduler {
		s := schedule.New(lock.New(locktest.NewStore()), schedule.DefaultConfig(), nil)
		s.Add("reports:daily", "@daily", noop)
		s.Add("sessions:cleanup", "@hourly", noop)
		return s
	}, mon)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for line := range strings.Lines(out.String()) {
		rows = append(rows, strings.Fields(line))
	}
	if len(rows) != 3 || strings.Join(rows[0], " ") != "TASK EXPRESSION NEXT RUN IN RUNNING LAST RUN RESULT TOOK DESCRIPTION" {
		t.Fatalf("rows %q", rows)
	}
	// 名称、表达式、下一次（日期、时间）、距现在、是否在执行、上一次（日期、时间）、结果、耗时。
	if got := strings.Join(rows[1][5:], " "); rows[1][0] != "reports:daily" || got != "no 2026-11-12 03:00:00 failed 1.5s" {
		t.Fatalf("reports %q", rows[1])
	}
	if got := strings.Join(rows[2][5:], " "); got != "no - - -" {
		t.Fatalf("cleanup %q", rows[2])
	}
}
