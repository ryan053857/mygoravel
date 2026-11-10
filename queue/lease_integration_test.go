//go:build integration

package queue

import (
	"context"
	"io/fs"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/postgres"
)

// 领取任务后进程崩溃：租约到期后任务被重新领取，旧的领取不能再确认或修改它。
func TestExpiredLeaseIsReclaimedAndFenced(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, postgresMigrations(t))
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
}

func postgresMigrations(t *testing.T) fs.FS {
	t.Helper()
	f, err := fs.Sub(Migrations(), postgres.Driver.Name())
	if err != nil {
		t.Fatal(err)
	}
	return f
}
