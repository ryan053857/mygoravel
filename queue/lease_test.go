package queue

import (
	"context"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

// 领取任务后进程崩溃：租约到期后任务被重新领取，旧的领取不能再确认或修改它。
func TestExpiredLeaseIsReclaimedAndFenced(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, Migrations())
		ctx := context.Background()
		cfg := DefaultConfig()
		cfg.Lease = 500 * time.Millisecond
		s := NewSubscriber(db, cfg, nil)

		if err := Publish(ctx, NewPublisher(db), "jobs", 1); err != nil {
			t.Fatal(err)
		}
		claimedAt := time.Now()
		crashed, err := s.claim("jobs", 10)
		if err != nil || len(crashed) != 1 || crashed[0].Attempts != 1 || crashed[0].ReservedAt.IsZero() {
			t.Fatalf("first claim %+v err=%v", crashed, err)
		}
		// 租约期内不能再次领取。调用慢到超过租约时（CI 上的 SQLite），租约已经正常过期，这次领取就是重新领取。
		reclaimed, err := s.claim("jobs", 10)
		if err != nil || len(reclaimed) != 0 && time.Since(claimedAt) < cfg.Lease {
			t.Fatalf("claimed a leased job: %+v err=%v", reclaimed, err)
		}
		if len(reclaimed) == 0 {
			time.Sleep(time.Until(crashed[0].AvailableAt) + 50*time.Millisecond)
			reclaimed, err = s.claim("jobs", 10)
		}
		if err != nil || len(reclaimed) != 1 || reclaimed[0].Attempts != 2 {
			t.Fatalf("reclaim %+v err=%v", reclaimed, err)
		}

		if ok, err := s.update(crashed[0], map[string]any{"available_at": time.Now().UTC()}); ok || err != nil {
			t.Fatalf("stale lease updated job: ok=%v err=%v", ok, err)
		}
		s.ack(ctx, crashed[0])
		if n, _ := db.NewSelect().Model((*job)(nil)).Count(ctx); n != 1 {
			t.Fatalf("stale ack deleted the job")
		}
		if err := s.fail(crashed[0], context.Canceled); err != nil {
			t.Fatal(err)
		}
		if n, _ := db.NewSelect().Model((*failedJob)(nil)).Count(ctx); n != 0 {
			t.Fatalf("stale fail moved the job")
		}
		s.ack(ctx, reclaimed[0])
		if n, _ := db.NewSelect().Model((*job)(nil)).Count(ctx); n != 0 {
			t.Fatalf("ack did not delete the job")
		}
	})
}
