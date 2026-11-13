package queuecmd_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/queue"
	"github.com/go-tenon/tenon/queue/queuecmd"
)

// run 执行命令，返回每行按空白分隔的字段。
func run(t *testing.T, db bun.IDB, args ...string) [][]string {
	t.Helper()
	out, err := execute(db, args...)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for line := range strings.Lines(out) {
		rows = append(rows, strings.Fields(line))
	}
	return rows
}

func execute(db bun.IDB, args ...string) (string, error) {
	cmd := queuecmd.New(db)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return out.String(), err
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
	rows := run(t, db, "failed", "--topic", "mail")
	if len(rows) != 3 || rows[0][0] != "ID" || rows[1][0] != recent.String() || rows[2][0] != old.String() {
		t.Fatalf("rows %q", rows)
	}
	if e := rows[1][len(rows[1])-1]; len([]rune(e)) != 80 || !strings.HasSuffix(e, "…") {
		t.Fatalf("long error %q", e)
	}
	if e := strings.Join(rows[2][5:], " "); e != "smtp down" {
		t.Fatalf("first line %q", e)
	}
	if rows := run(t, db, "failed", "--limit", "1"); len(rows) != 2 || rows[1][0] != recent.String() {
		t.Fatalf("limited rows %q", rows)
	}
}

// status 返回 queue status 输出中 key 一行的值。
func status(t *testing.T, db bun.IDB, key string) string {
	t.Helper()
	out, err := execute(db, "status")
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(out) {
		if v, ok := strings.CutPrefix(line, key+"  "); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// 复制槽可用时 recreate 拒绝重建；丢失后重建，状态恢复。
func TestStatusAndRecreate(t *testing.T) {
	db := dbtest.Open(t, queue.Migrations())
	ctx := context.Background()
	if s := status(t, db, "state"); !strings.HasPrefix(s, "idle") {
		t.Fatalf("state %q", s)
	}
	if s := status(t, db, "wal status"); s != "reserved" {
		t.Fatalf("wal status %q", s)
	}
	at := time.Now().Add(48 * time.Hour)
	if _, err := db.ExecContext(ctx, `INSERT INTO queue_scheduled (id, topic, body, available_at, created_at)
		VALUES (?, 'mail', '{}', ?, now())`, uuid.New(), at.UTC()); err != nil {
		t.Fatal(err)
	}
	if s := status(t, db, "scheduled"); s != "1" {
		t.Fatalf("scheduled %q", s)
	}
	if s := status(t, db, "next scheduled"); s != at.Local().Format(time.DateTime) {
		t.Fatalf("next scheduled %q", s)
	}

	if _, err := execute(db, "slot", "recreate"); !errors.Is(err, queue.ErrSlotHealthy) {
		t.Fatalf("recreate a healthy slot: %v", err)
	}
	if _, err := db.ExecContext(ctx, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE database = current_database()"); err != nil {
		t.Fatal(err)
	}
	if s := status(t, db, "state"); !strings.HasPrefix(s, "missing") {
		t.Fatalf("state %q", s)
	}
	run(t, db, "slot", "recreate")
	if s := status(t, db, "state"); !strings.HasPrefix(s, "idle") {
		t.Fatalf("state after recreate %q", s)
	}
	run(t, db, "slot", "recreate", "--force")
}
