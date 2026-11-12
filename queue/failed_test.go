package queue_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
)

func insertFailed(t *testing.T, db bun.IDB, topic string, age time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(), `INSERT INTO queue_failed_jobs (id, topic, payload, metadata, attempts, error, failed_at)
		VALUES (?, ?, ?, '{"request_id":"r1"}', 3, 'boom', ?)`, id, topic, []byte(`{"n":1}`), time.Now().UTC().Add(-age))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCleanupFailedJobs(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	ctx := context.Background()
	for _, age := range []time.Duration{72 * time.Hour, 48 * time.Hour, 30 * time.Hour, time.Hour} {
		insertFailed(t, db, "jobs", age)
	}
	cleanup := queue.NewFailedJobs(db).Cleanup(24 * time.Hour)
	if _, err := cleanup(ctx, 0); err == nil {
		t.Fatal("expected error for non-positive limit")
	}
	if n, err := cleanup(ctx, 2); n != 2 || err != nil {
		t.Fatalf("first cleanup n=%d err=%v", n, err)
	}
	if n, err := cleanup(ctx, 10); n != 1 || err != nil {
		t.Fatalf("second cleanup n=%d err=%v", n, err)
	}
	if rows := failedRows(t, db); len(rows) != 1 {
		t.Fatalf("remaining %+v", rows)
	}
}

func TestFailedJobsQueries(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	ctx := context.Background()
	ids := []uuid.UUID{insertFailed(t, db, "mail", 3*time.Minute), insertFailed(t, db, "mail", 2*time.Minute), insertFailed(t, db, "report", time.Minute)}
	failed := queue.NewFailedJobs(db)

	all, err := failed.List(ctx, queue.FailedQuery{})
	if err != nil || len(all) != 3 || all[0].ID != ids[2] || all[2].ID != ids[0] {
		t.Fatalf("list %+v err %v", all, err)
	}
	if j := all[0]; j.Topic != "report" || j.Attempts != 3 || j.Error != "boom" || j.Metadata["request_id"] != "r1" {
		t.Fatalf("job %+v", j)
	}
	mail, err := failed.List(ctx, queue.FailedQuery{Topic: "mail", Limit: 1, Offset: 1})
	if err != nil || len(mail) != 1 || mail[0].ID != ids[0] {
		t.Fatalf("mail page %+v err %v", mail, err)
	}
	if j, err := failed.Get(ctx, ids[1]); err != nil || j.ID != ids[1] || j.Topic != "mail" {
		t.Fatalf("get %+v err %v", j, err)
	}
	if err := failed.Delete(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := failed.Get(ctx, ids[1]); !errors.Is(err, queue.ErrFailedJobNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
	if err := failed.Delete(ctx, ids[1]); !errors.Is(err, queue.ErrFailedJobNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

// RetryAll 分批重新发布 before 之前失败的任务；DeleteAll 分批删除。
func TestFailedJobsBulk(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := range 3 {
		insertFailed(t, db, "mail", time.Duration(i+2)*time.Hour)
	}
	insertFailed(t, db, "report", time.Hour)         // 其他 topic
	fresh := insertFailed(t, db, "mail", -time.Hour) // before 之后失败
	failed := queue.NewFailedJobs(db)

	for _, bad := range []func() (int, error){
		func() (int, error) { return failed.RetryAll(ctx, "mail", now, 0) },
		func() (int, error) { return failed.RetryAll(ctx, "mail", time.Time{}, 10) },
		func() (int, error) { return failed.DeleteAll(ctx, "mail", now, 0) },
		func() (int, error) { return failed.DeleteAll(ctx, "mail", time.Time{}, 10) },
	} {
		if _, err := bad(); err == nil {
			t.Fatal("expected an argument error")
		}
	}
	for _, want := range []int{2, 1, 0} {
		if n, err := failed.RetryAll(ctx, "mail", now, 2); n != want || err != nil {
			t.Fatalf("batch n=%d err=%v, want %d", n, err, want)
		}
	}
	if n := len(queuetest.Pending(t, db, queue.Topic[json.RawMessage]("mail"))); n != 3 {
		t.Fatalf("%d jobs published", n)
	}
	// 全部 topic：删除 before 之前的，before 之后失败的保留。
	if n, err := failed.DeleteAll(ctx, "", now, 10); n != 1 || err != nil {
		t.Fatalf("delete n=%d err=%v", n, err)
	}
	if rows := failedRows(t, db); len(rows) != 1 || rows[0].ID != fresh {
		t.Fatalf("remaining %+v", rows)
	}
}
