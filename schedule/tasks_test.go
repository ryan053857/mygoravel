package schedule_test

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/schedule"
)

func TestTasks(t *testing.T) {
	s := newScheduler()
	shanghai := time.FixedZone("CST", 8*3600)
	s.Add("reports:daily", "0 3 * * *", func(context.Context) error { return nil },
		schedule.Describe("生成前一天的报表"), schedule.In(shanghai))
	s.Add("tmp:cleanup", "@every 15s", func(context.Context) error { return nil }, schedule.EveryInstance(), schedule.AllowOverlap())

	tasks := s.Tasks()
	if len(tasks) != 2 {
		t.Fatalf("%d tasks", len(tasks))
	}
	daily, tmp := tasks[0], tasks[1]
	if daily.Name != "reports:daily" || daily.Description != "生成前一天的报表" || daily.Expr != "0 3 * * *" ||
		daily.Location != shanghai || daily.EveryInstance || daily.AllowOverlap {
		t.Fatalf("daily %+v", daily)
	}
	if tmp.Description != "" || !tmp.EveryInstance || !tmp.AllowOverlap || tmp.Location != time.Local {
		t.Fatalf("tmp %+v", tmp)
	}

	// base 是 UTC 03:00，即上海 11:00：上一次是当天上海 03:00，下一次是次日上海 03:00。
	if got, want := daily.Prev(base), time.Date(2026, 11, 11, 3, 0, 0, 0, shanghai); !got.Equal(want) {
		t.Errorf("daily prev %v, want %v", got, want)
	}
	if got, want := daily.Next(base), time.Date(2026, 11, 12, 3, 0, 0, 0, shanghai); !got.Equal(want) {
		t.Errorf("daily next %v, want %v", got, want)
	}
	// @every 按 Unix 时间对齐；Next 不含 after 所在的这一秒，Prev 包含。
	if got := tmp.Next(base); !got.Equal(base.Add(15 * time.Second)) {
		t.Errorf("every next %v", got)
	}
	if got := tmp.Prev(base.Add(500 * time.Millisecond)); !got.Equal(base) {
		t.Errorf("every prev %v", got)
	}
	if got := tmp.Next(base.Add(14*time.Second + 999*time.Millisecond)); !got.Equal(base.Add(15 * time.Second)) {
		t.Errorf("every next %v", got)
	}
}

// Next 和 Prev 与调度器实际触发的时刻一致：逐秒用 RunDue 找出到期的时刻作为参照。
func TestNextPrevMatchDue(t *testing.T) {
	ctx := context.Background()
	for _, expr := range []string{"* * * * *", "*/10 * * * * *", "15,45 * * * * *", "@every 7s", "*/2 * * * *", "@everysecond"} {
		s := newScheduler()
		var due []time.Time
		var tick time.Time
		s.Add("t", expr, func(context.Context) error { due = append(due, tick); return nil }, schedule.AllowOverlap(), schedule.EveryInstance(), schedule.In(time.UTC))
		start := base.Add(-37 * time.Second)
		for at := start; at.Before(start.Add(5 * time.Minute)); at = at.Add(time.Second) {
			tick = at
			if err := s.RunDue(ctx, at); err != nil {
				t.Fatal(err)
			}
		}
		task := s.Tasks()[0]
		for i := 1; i < len(due); i++ {
			// 两个相邻的到期时刻之间的任意时间，Next 都是后一个，Prev 都是前一个。
			for _, at := range []time.Time{due[i-1], due[i-1].Add(500 * time.Millisecond), due[i].Add(-time.Second)} {
				if at.Before(due[i-1]) || !at.Before(due[i]) {
					continue
				}
				if got := task.Next(at); !got.Equal(due[i]) {
					t.Fatalf("%s: Next(%v) = %v, want %v", expr, at, got, due[i])
				}
				if got := task.Prev(at); !got.Equal(due[i-1]) {
					t.Fatalf("%s: Prev(%v) = %v, want %v", expr, at, got, due[i-1])
				}
			}
		}
		if len(due) < 2 {
			t.Fatalf("%s: due %d times", expr, len(due))
		}
	}
}

func TestRunning(t *testing.T) {
	ctx := context.Background()
	locker := lock.New(lock.NewMemoryStore())
	quiet := slog.New(slog.DiscardHandler)
	release, started := make(chan struct{}), make(chan struct{})
	register := func(s *schedule.Scheduler) {
		s.Add("slow", "@everysecond", func(context.Context) error {
			close(started)
			<-release
			return nil
		})
		s.Add("local", "@everysecond", func(context.Context) error { return nil }, schedule.EveryInstance())
	}
	viewer := schedule.New(locker, schedule.DefaultConfig(), quiet) // 例如 HTTP 进程中只用来查看的调度器
	// 收到结束事件时，执行锁已经释放。
	runningAtFinish := make(chan bool, 1)
	runner := schedule.New(locker, schedule.DefaultConfig(), quiet, schedule.WithObserver(func(ctx context.Context, e schedule.Event) {
		if e.Task == "slow" && e.Kind == schedule.TaskSucceeded {
			running, _ := viewer.Running(ctx, "slow")
			runningAtFinish <- running
		}
	}))
	register(runner)
	register(viewer)

	if running, err := viewer.Running(ctx, "slow"); err != nil || running {
		t.Fatalf("before: running=%v err=%v", running, err)
	}
	done := make(chan error, 1)
	go func() { done <- runner.RunDue(ctx, base) }()
	<-started
	if running, err := viewer.Running(ctx, "slow"); err != nil || !running {
		t.Fatalf("during: running=%v err=%v", running, err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if running, err := viewer.Running(ctx, "slow"); err != nil || running {
		t.Fatalf("after: running=%v err=%v", running, err)
	}
	if <-runningAtFinish {
		t.Fatal("still running when the finished event was observed")
	}
	if _, err := viewer.Running(ctx, "local"); !errors.Is(err, schedule.ErrNotTracked) {
		t.Fatalf("every instance: %v", err)
	}
	if _, err := viewer.Running(ctx, "nope"); !errors.Is(err, schedule.ErrUnknownTask) {
		t.Fatalf("unknown: %v", err)
	}
}

func TestObserver(t *testing.T) {
	ctx := context.Background()
	var mu sync.Mutex
	var events []schedule.Event
	record := func(_ context.Context, e schedule.Event) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, e)
	}
	s := schedule.New(lock.New(lock.NewMemoryStore()), schedule.DefaultConfig(), slog.New(slog.DiscardHandler),
		schedule.WithObserver(record), schedule.WithObserver(func(context.Context, schedule.Event) { panic("ignored") }))
	boom := errors.New("boom")
	release, started := make(chan struct{}), make(chan struct{})
	s.Add("ok", "@every 2s", func(context.Context) error { return nil })
	s.Add("fails", "@every 2s", func(context.Context) error { return boom })
	s.Add("slow", "@every 2s", func(context.Context) error {
		close(started)
		<-release
		return nil
	})
	done := make(chan error, 1)
	go func() { done <- s.RunDue(ctx, base) }()
	<-started
	// 等第一次触发的 ok 和 fails 结束；slow 还在执行，下一次触发被跳过。
	finished := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, e := range events {
			if e.Kind == schedule.TaskSucceeded || e.Kind == schedule.TaskFailed {
				n++
			}
		}
		return n
	}
	for deadline := time.Now().Add(5 * time.Second); finished() < 2; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("first run did not finish")
		}
	}
	if err := s.RunDue(ctx, base.Add(2*time.Second)); !errors.Is(err, boom) {
		t.Fatalf("second run: %v", err)
	}
	close(release)
	if err := <-done; !errors.Is(err, boom) {
		t.Fatalf("first run: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	got := map[string][]string{}
	for _, e := range events {
		desc := e.Kind.String() + "@" + e.Tick.Sub(base).String()
		if e.SkipReason != "" {
			desc += ":" + string(e.SkipReason)
		}
		got[e.Task] = append(got[e.Task], desc)
		switch e.Kind {
		case schedule.TaskStarted:
			if e.Started.IsZero() || e.Duration != 0 {
				t.Errorf("started %+v", e)
			}
		case schedule.TaskSucceeded, schedule.TaskFailed:
			if e.Started.IsZero() || e.Duration <= 0 || (e.Kind == schedule.TaskFailed) != errors.Is(e.Err, boom) {
				t.Errorf("finished %+v", e)
			}
		case schedule.TaskSkipped:
			if !e.Started.IsZero() {
				t.Errorf("skipped %+v", e)
			}
		}
	}
	want := map[string]string{
		"ok":    "started@0s succeeded@0s started@2s succeeded@2s",
		"fails": "started@0s failed@0s started@2s failed@2s",
		"slow":  "started@0s skipped@2s:overlap succeeded@0s",
	}
	for task, w := range want {
		if strings.Join(got[task], " ") != w {
			t.Errorf("%s: %v, want %s", task, got[task], w)
		}
	}
}
