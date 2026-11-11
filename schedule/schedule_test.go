package schedule_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/lock"
	"github.com/ryan053857/mygoravel/lock/locktest"
	"github.com/ryan053857/mygoravel/schedule"
)

func newScheduler() *schedule.Scheduler {
	return schedule.New(lock.New(locktest.NewStore()), schedule.DefaultConfig(), nil)
}

// counter 返回一个记录执行次数的任务。
func counter(n *atomic.Int64) func(context.Context) error {
	return func(context.Context) error {
		n.Add(1)
		return nil
	}
}

var base = time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)

func TestExpressions(t *testing.T) {
	cases := []struct {
		expr string
		due  []time.Time
		not  []time.Time
	}{
		{"0 3 * * *", []time.Time{base}, []time.Time{base.Add(time.Second), base.Add(time.Minute)}},
		{"*/10 * * * * *", []time.Time{base, base.Add(10 * time.Second)}, []time.Time{base.Add(5 * time.Second)}},
		{"@hourly", []time.Time{base}, []time.Time{base.Add(time.Minute)}},
		{"@every 15s", []time.Time{base, base.Add(15 * time.Second), base.Add(45 * time.Second)}, []time.Time{base.Add(10 * time.Second)}},
		{"@every 90s", []time.Time{base, base.Add(90 * time.Second)}, []time.Time{base.Add(time.Minute)}},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			var n atomic.Int64
			s := newScheduler()
			s.Add("task", tc.expr, counter(&n), schedule.In(time.UTC), schedule.EveryInstance())
			for _, at := range tc.due {
				before := n.Load()
				if err := s.RunDue(context.Background(), at.Add(300*time.Millisecond)); err != nil {
					t.Fatal(err)
				}
				if n.Load() != before+1 {
					t.Errorf("not due at %s", at.Format(time.TimeOnly))
				}
			}
			for _, at := range tc.not {
				before := n.Load()
				_ = s.RunDue(context.Background(), at)
				if n.Load() != before {
					t.Errorf("due at %s", at.Format(time.TimeOnly))
				}
			}
		})
	}
}

func TestInvalidTasks(t *testing.T) {
	noop := func(context.Context) error { return nil }
	for _, expr := range []string{"bad", "* * *", "@every 500ms", "@every 1.5s", "@every x"} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Add(%q) did not panic", expr)
				}
			}()
			newScheduler().Add("task", expr, noop)
		}()
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("overlong name did not panic")
			}
		}()
		newScheduler().Add(strings.Repeat("x", schedule.MaxNameLength+1), "@hourly", noop)
	}()
	s := newScheduler()
	s.Add("task", "@hourly", noop)
	defer func() {
		if recover() == nil {
			t.Error("duplicate name did not panic")
		}
	}()
	s.Add("task", "@daily", noop)
}

func TestLocation(t *testing.T) {
	shanghai := time.FixedZone("CST", 8*3600)
	var n atomic.Int64
	s := newScheduler()
	s.Add("task", "0 3 * * *", counter(&n), schedule.In(shanghai))
	_ = s.RunDue(context.Background(), base) // UTC 03:00 是上海 11:00
	_ = s.RunDue(context.Background(), time.Date(2026, 11, 11, 3, 0, 0, 0, shanghai))
	if n.Load() != 1 {
		t.Fatalf("ran %d times", n.Load())
	}
}

func TestRunsOncePerTickAcrossInstances(t *testing.T) {
	locker := lock.New(locktest.NewStore())
	var once, every atomic.Int64
	a, b := schedule.New(locker, schedule.DefaultConfig(), nil), schedule.New(locker, schedule.DefaultConfig(), nil)
	for _, s := range []*schedule.Scheduler{a, b} {
		s.Add("once", "@everysecond", counter(&once))
		s.Add("every", "@everysecond", counter(&every), schedule.EveryInstance())
	}
	for i := range 3 {
		at := base.Add(time.Duration(i) * time.Second)
		_ = a.RunDue(context.Background(), at)
		_ = b.RunDue(context.Background(), at.Add(400*time.Millisecond)) // 时钟慢一些的实例
	}
	if once.Load() != 3 || every.Load() != 6 {
		t.Fatalf("once=%d every=%d", once.Load(), every.Load())
	}
}

// blockingTask 在 started 收到信号后一直执行，直到 release 关闭。
func blockingTask(runs *atomic.Int64, started chan<- struct{}, release <-chan struct{}) func(context.Context) error {
	return func(context.Context) error {
		runs.Add(1)
		started <- struct{}{}
		<-release
		return nil
	}
}

func TestOverlap(t *testing.T) {
	for _, tc := range []struct {
		name      string
		opts      []schedule.Option
		instances int
		want      int64
	}{
		{"same instance skipped", nil, 1, 1},
		{"other instance skipped", nil, 2, 1},
		{"allowed", []schedule.Option{schedule.AllowOverlap()}, 1, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			locker := lock.New(locktest.NewStore())
			var runs atomic.Int64
			started := make(chan struct{}, 2)
			release := make(chan struct{})
			scheds := make([]*schedule.Scheduler, tc.instances)
			for i := range scheds {
				scheds[i] = schedule.New(locker, schedule.DefaultConfig(), nil)
				scheds[i].Add("report", "@everysecond", blockingTask(&runs, started, release), tc.opts...)
			}
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = scheds[0].RunDue(context.Background(), base)
			}()
			<-started
			// 下一秒触发时第一次还没结束。
			second := make(chan struct{})
			go func() {
				_ = scheds[len(scheds)-1].RunDue(context.Background(), base.Add(time.Second))
				close(second)
			}()
			if tc.want == 2 {
				<-started
			} else {
				<-second
			}
			close(release)
			wg.Wait()
			<-second
			if runs.Load() != tc.want {
				t.Fatalf("runs = %d, want %d", runs.Load(), tc.want)
			}
		})
	}
}

func TestFailuresAndPanics(t *testing.T) {
	boom := errors.New("boom")
	s := newScheduler()
	s.Add("fails", "@everysecond", func(context.Context) error { return boom })
	s.Add("panics", "@everysecond", func(context.Context) error { panic("bad") })
	err := s.RunDue(context.Background(), base)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "panic: bad") {
		t.Fatalf("err = %v", err)
	}
	// 失败的任务下一次照常执行。
	if err := s.RunDue(context.Background(), base.Add(time.Second)); !errors.Is(err, boom) {
		t.Fatalf("second run err = %v", err)
	}
}

func TestRun(t *testing.T) {
	var n atomic.Int64
	s := newScheduler()
	s.Add("tick", "@everysecond", counter(&n))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	time.Sleep(2500 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := n.Load(); got < 2 || got > 3 {
		t.Fatalf("ran %d times in 2.5s", got)
	}
	if err := s.Run(context.Background()); err == nil {
		t.Fatal("second Run should fail")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("Add after Run did not panic")
		}
	}()
	s.Add("late", "@hourly", counter(&n))
}

func TestCleanup(t *testing.T) {
	var calls []int
	remaining := 25
	task := schedule.Cleanup(func(_ context.Context, limit int) (int, error) {
		n := min(limit, remaining)
		remaining -= n
		calls = append(calls, n)
		return n, nil
	}, 10)
	if err := task(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 3 || remaining != 0 {
		t.Fatalf("calls %v remaining %d", calls, remaining)
	}
	boom := errors.New("boom")
	failing := schedule.Cleanup(func(context.Context, int) (int, error) { return 0, boom }, 10)
	if err := failing(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestConfig(t *testing.T) {
	var cfg schedule.Config
	cfg.SetDefaults()
	if cfg.ShutdownTimeout != 30*time.Second || cfg.Validate() != nil {
		t.Fatalf("defaults %+v: %v", cfg, cfg.Validate())
	}
	if (schedule.Config{}).Validate() != nil {
		t.Fatal("zero shutdown_timeout must be allowed")
	}
	bad := schedule.Config{ShutdownTimeout: -time.Second}
	if bad.Validate() == nil {
		t.Fatal("negative shutdown_timeout accepted")
	}
	defer func() {
		if recover() == nil {
			t.Fatal("New with invalid config did not panic")
		}
	}()
	schedule.New(lock.New(locktest.NewStore()), bad, nil)
}
