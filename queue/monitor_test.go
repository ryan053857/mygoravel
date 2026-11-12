package queue_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/queue"
)

func TestMonitor(t *testing.T) {
	ctx := context.Background()
	db := openDB(t, sqlite.Driver)
	mon := monitor.New(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()), nil)
	t.Cleanup(func() { _ = mon.Close(ctx) })

	var attempts atomic.Int64
	var done atomic.Int64
	stop := start(t, db, testConfig(), func(w *queue.Worker) {
		jobs.Handle(w, func(ctx context.Context, p payload) error {
			defer done.Add(1)
			switch {
			case p.N == 2:
				return queue.Permanent(errors.New("gone"))
			case p.N == 3 && attempts.Add(1) == 1:
				return queue.RetryAfter(errors.New("busy"), time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			return nil
		})
	}, queue.WithMonitor(mon))
	for n := range 3 {
		publish(t, db, n+1)
	}
	eventually(t, 5*time.Second, func() bool { return done.Load() == 4 })
	stop()
	if err := mon.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	topics, err := queue.Activity(ctx, mon, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a := topics["jobs"]
	if len(topics) != 1 || a.Succeeded.Count != 2 || a.Retried.Count != 1 || a.Failed.Count != 1 || a.Wait.Count != 4 {
		t.Fatalf("activity %+v", topics)
	}
	runs := a.Executions()
	if runs.Count != 4 || runs.MaxDuration() < 20*time.Millisecond || runs.MeanDuration() > runs.MaxDuration() || a.Wait.MaxDuration() < a.Wait.MeanDuration() {
		t.Fatalf("durations %+v", a)
	}
}
