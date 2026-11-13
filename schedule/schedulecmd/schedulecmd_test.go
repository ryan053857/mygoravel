package schedulecmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/go-tenon/tenon/lock"
	"github.com/go-tenon/tenon/lock/locktest"
	"github.com/go-tenon/tenon/schedule"
	"github.com/go-tenon/tenon/schedule/schedulecmd"
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
	cmd := schedulecmd.New(tasks)
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
