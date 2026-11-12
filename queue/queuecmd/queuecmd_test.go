package queuecmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuecmd"
)

// run 执行命令，返回每行按空白分隔的字段。
func run(t *testing.T, failed *queue.FailedJobs, args ...string) [][]string {
	t.Helper()
	cmd := queuecmd.New(failed)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for line := range strings.Lines(out.String()) {
		rows = append(rows, strings.Fields(line))
	}
	return rows
}

func insertFailed(t *testing.T, db *bun.DB, topic, msg string, at time.Time) uuid.UUID {
	t.Helper()
	id := uuid.New()
	_, err := db.ExecContext(context.Background(), `INSERT INTO queue_failed_jobs (id, topic, payload, attempts, error, failed_at)
		VALUES (?, ?, ?, 3, ?, ?)`, id, topic, []byte("{}"), msg, at.UTC())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestFailed(t *testing.T) {
	db := dbtest.Open(t, queue.Migrations())
	now := time.Now()
	old := insertFailed(t, db, "mail", "smtp down\nstack trace", now.Add(-time.Hour))
	recent := insertFailed(t, db, "mail", strings.Repeat("长", 100), now)
	insertFailed(t, db, "report", "boom", now.Add(-time.Minute))
	failed := queue.NewFailedJobs(db)

	rows := run(t, failed, "failed", "--topic", "mail")
	if len(rows) != 3 || rows[0][0] != "ID" || rows[1][0] != recent.String() || rows[2][0] != old.String() {
		t.Fatalf("rows %q", rows)
	}
	if e := rows[1][len(rows[1])-1]; len([]rune(e)) != 80 || !strings.HasSuffix(e, "…") {
		t.Fatalf("long error %q", e)
	}
	if e := strings.Join(rows[2][5:], " "); e != "smtp down" {
		t.Fatalf("first line %q", e)
	}
	if rows := run(t, failed, "failed", "--limit", "1"); len(rows) != 2 || rows[1][0] != recent.String() {
		t.Fatalf("limited rows %q", rows)
	}
}
