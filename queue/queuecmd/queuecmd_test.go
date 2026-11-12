package queuecmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuecmd"
)

var mail = queue.Topic[int]("mail")

// run 执行命令，返回每行按空白分隔的字段。
func run(t *testing.T, failed *queue.FailedJobs, mon *monitor.Monitor, drivers []queue.Driver, args ...string) [][]string {
	t.Helper()
	cmd := queuecmd.New(failed, mon, drivers...)
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
	_, err := db.ExecContext(context.Background(), `INSERT INTO queue_failed_jobs (id, driver, topic, payload, attempts, error, failed_at)
		VALUES (?, 'database', ?, ?, 3, ?, ?)`, id, topic, []byte("{}"), msg, at.UTC())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestStats(t *testing.T) {
	db := dbtesting.Open(t, sqlite.Driver, queue.Migrations())
	ctx := context.Background()
	d, failed := queue.Database(db), queue.NewFailedJobs(db)
	for i := range 2 {
		if err := mail.Publish(ctx, d, i); err != nil {
			t.Fatal(err)
		}
	}
	if err := mail.Publish(ctx, d, 3, queue.Delay(time.Hour)); err != nil {
		t.Fatal(err)
	}
	insertFailed(t, db, "report", "boom", time.Now())

	rows := run(t, failed, nil, []queue.Driver{d}, "stats")
	want := [][]string{
		{"TOPIC", "READY", "DELAYED", "RETRYING", "RUNNING", "FAILED", "WAITING"},
		{"mail", "2", "1", "0", "0", "0"},
		{"report", "0", "0", "0", "0", "1", "0s"},
	}
	if len(rows) != 3 || strings.Join(rows[0], " ") != strings.Join(want[0], " ") ||
		strings.Join(rows[1][:6], " ") != strings.Join(want[1], " ") || strings.Join(rows[2], " ") != strings.Join(want[2], " ") {
		t.Fatalf("rows %q", rows)
	}
	if wait, err := time.ParseDuration(rows[1][6]); err != nil || wait > time.Minute {
		t.Fatalf("waiting %q", rows[1][6])
	}
}

// partial 是只给出 ready 和 running 的驱动。
type partial struct{ queue.Driver }

func (partial) Name() string { return "partial" }

func (partial) Stats(context.Context) ([]queue.TopicStats, error) {
	return []queue.TopicStats{{Topic: "events", Jobs: map[queue.State]int{queue.StateReady: 4, queue.StateRunning: 1}}}, nil
}

func TestStatsMultipleDrivers(t *testing.T) {
	db := dbtesting.Open(t, sqlite.Driver, queue.Migrations())
	rows := run(t, queue.NewFailedJobs(db), nil, []queue.Driver{queue.Database(db), partial{}}, "stats")
	want := "DRIVER TOPIC READY DELAYED RETRYING RUNNING FAILED WAITING | partial events 4 - - 1 0 -"
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = strings.Join(r, " ")
	}
	if strings.Join(got, " | ") != want {
		t.Fatalf("got %q", got)
	}
}

func TestFailed(t *testing.T) {
	db := dbtesting.Open(t, sqlite.Driver, queue.Migrations())
	now := time.Now()
	old := insertFailed(t, db, "mail", "smtp down\nstack trace", now.Add(-time.Hour))
	recent := insertFailed(t, db, "mail", strings.Repeat("长", 100), now)
	insertFailed(t, db, "report", "boom", now.Add(-time.Minute))
	failed := queue.NewFailedJobs(db)
	drivers := []queue.Driver{queue.Database(db)}

	rows := run(t, failed, nil, drivers, "failed", "--topic", "mail")
	if len(rows) != 3 || rows[0][0] != "ID" || rows[1][0] != recent.String() || rows[2][0] != old.String() {
		t.Fatalf("rows %q", rows)
	}
	if e := rows[1][len(rows[1])-1]; len([]rune(e)) != 80 || !strings.HasSuffix(e, "…") {
		t.Fatalf("long error %q", e)
	}
	if e := strings.Join(rows[2][5:], " "); e != "smtp down" {
		t.Fatalf("first line %q", e)
	}
	if rows := run(t, failed, nil, drivers, "failed", "--limit", "1"); len(rows) != 2 || rows[1][0] != recent.String() {
		t.Fatalf("limited rows %q", rows)
	}
}

func TestStatsActivity(t *testing.T) {
	ctx := context.Background()
	db := dbtesting.Open(t, sqlite.Driver, queue.Migrations())
	mon := monitor.New(dbtesting.Open(t, sqlite.Driver, monitor.Migrations()), nil)
	t.Cleanup(func() { _ = mon.Close(ctx) })
	mon.RecordDuration(queue.MetricSucceeded, "mail", 200*time.Millisecond)
	mon.RecordDuration(queue.MetricSucceeded, "mail", 400*time.Millisecond)
	mon.RecordDuration(queue.MetricFailed, "mail", 600*time.Millisecond)
	mon.RecordDuration(queue.MetricWait, "mail", 1500*time.Millisecond)
	mon.RecordDuration(queue.MetricSucceeded, "report", 2*time.Second)
	if err := mon.Flush(ctx); err != nil {
		t.Fatal(err)
	}

	rows := run(t, queue.NewFailedJobs(db), mon, []queue.Driver{queue.Database(db)}, "stats", "--since", "30m")
	got := make([]string, len(rows))
	for i, r := range rows {
		got[i] = strings.Join(r, " ")
	}
	want := []string{
		"TOPIC READY DELAYED RETRYING RUNNING FAILED WAITING", // 队列中没有任务
		"",
		"最近 30m",
		"TOPIC SUCCEEDED RETRIED FAILED AVG TIME MAX TIME AVG WAIT MAX WAIT",
		"mail 2 0 1 400ms 600ms 1.5s 1.5s",
		"report 1 0 0 2s 2s - -", // 没有等待时长的记录
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}

	cmd := queuecmd.New(queue.NewFailedJobs(db), mon, queue.Database(db))
	cmd.SetArgs([]string{"stats", "--since", "0s"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.ExecuteContext(ctx); command.ExitCode(err) != command.ExitUsage {
		t.Fatalf("--since 0s: %v", err)
	}
}
