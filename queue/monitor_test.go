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
	stats := monitor.NewStore(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()))
	rec := monitor.NewRecorder(stats, nil)
	t.Cleanup(func() { _ = rec.Close(ctx) })

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
	}, queue.WithMonitor(rec))
	for n := range 3 {
		publish(t, db, n+1)
	}
	eventually(t, 5*time.Second, func() bool { return done.Load() == 4 })
	stop()
	if err := rec.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	topics, err := queue.Activity(ctx, stats, now.Add(-time.Hour), now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	a := topics["jobs"]
	if len(topics) != 1 || a.Succeeded != 2 || a.Retried != 1 || a.Failed != 1 {
		t.Fatalf("activity %+v", topics)
	}
	if a.MaxDuration < 20*time.Millisecond || a.MeanDuration <= 0 || a.MeanDuration > a.MaxDuration || a.MaxWait < a.MeanWait {
		t.Fatalf("durations %+v", a)
	}
}
