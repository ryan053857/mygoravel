package queue

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/sqlite"
)

// 领取任务后进程崩溃：租约到期后任务被重新领取，旧的领取不能再确认或修改它。
func TestExpiredLeaseIsReclaimedAndFenced(t *testing.T) {
	forEachDriver(t, func(t *testing.T, driver database.Driver) {
		db := dbtest.Open(t, driver, migrations(t, driver))
		ctx := context.Background()
		cfg := DefaultConfig()
		cfg.Lease = 200 * time.Millisecond
		s := NewSubscriber(db, cfg, nil)

		if err := Publish(ctx, NewPublisher(db), "jobs", 1); err != nil {
			t.Fatal(err)
		}
		crashed, err := s.claim("jobs", 10)
		if err != nil || len(crashed) != 1 || crashed[0].Attempts != 1 || crashed[0].ReservedAt.IsZero() {
			t.Fatalf("first claim %+v err=%v", crashed, err)
		}
		if again, err := s.claim("jobs", 10); err != nil || len(again) != 0 {
			t.Fatalf("claimed a leased job: %+v err=%v", again, err)
		}

		time.Sleep(250 * time.Millisecond)
		reclaimed, err := s.claim("jobs", 10)
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

// testDrivers 是租约测试运行的数据库，integration 标签追加 PostgreSQL。
var testDrivers = []database.Driver{sqlite.Driver}

func forEachDriver(t *testing.T, test func(t *testing.T, driver database.Driver)) {
	t.Helper()
	for _, driver := range testDrivers {
		t.Run(driver.Name(), func(t *testing.T) { test(t, driver) })
	}
}

func migrations(t *testing.T, driver database.Driver) fs.FS {
	t.Helper()
	f, err := fs.Sub(Migrations(), driver.Name())
	if err != nil {
		t.Fatal(err)
	}
	return f
}
