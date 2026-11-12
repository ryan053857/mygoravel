package queue_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
)

// 只有提交的任务被发布：事务回滚、保存点回滚中的任务都不会出现；不在事务中发布时立即生效。
func TestPendingCommittedOnly(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	ctx := context.Background()
	publish(t, db, 1)
	rollback := errors.New("rollback")
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := jobs.Publish(ctx, tx, payload{N: 2}); err != nil {
			return err
		}
		// 保存点中的任务随保存点回滚，外层事务照常提交。
		err := tx.RunInTx(ctx, nil, func(ctx context.Context, sp bun.Tx) error {
			if err := jobs.Publish(ctx, sp, payload{N: 3}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			return err
		}
		return jobs.Publish(ctx, tx, payload{N: 4}, queue.Delay(48*time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := jobs.Publish(ctx, tx, payload{N: 5}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	var got []int
	for _, p := range queuetest.Pending(t, db, jobs) {
		got = append(got, p.N)
	}
	if !slices.Equal(got, []int{1, 2, 4}) {
		t.Fatalf("pending %v", got)
	}
	if other := queuetest.Pending(t, db, queue.Topic[payload]("other")); len(other) != 0 {
		t.Fatalf("other topic %v", other)
	}
}

// 多个 Worker 中只有一个在转发；它停止后，待命的 Worker 接手，从确认的位置继续，任务不丢失。
func TestRelayFailover(t *testing.T) {
	queue.SetRelayRetry(t, 200*time.Millisecond)
	db := openDB(t)
	cfg := queuetest.Config(t)
	var mu sync.Mutex
	seen := map[int]int{}
	handle := func(w *queue.Worker) {
		jobs.Handle(w, func(_ context.Context, p payload) error {
			mu.Lock()
			seen[p.N]++
			mu.Unlock()
			return nil
		})
	}
	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(seen)
	}
	first := start(t, db, cfg, handle)
	eventually(t, 5*time.Second, func() bool { return slotActive(t, db) })
	start(t, db, cfg, handle)
	publish(t, db, 1)
	eventually(t, 5*time.Second, func() bool { return count() == 1 })

	first()
	for i := 2; i <= 50; i++ {
		publish(t, db, i)
	}
	eventually(t, 10*time.Second, func() bool { return count() == 50 })
	mu.Lock()
	defer mu.Unlock()
	for n, c := range seen {
		if c != 1 {
			t.Fatalf("job %d handled %d times", n, c)
		}
	}
}

// 复制槽不存在（没有执行迁移或被删除）时 Run 返回错误，不会悄悄丢失任务。
func TestRunWithoutSlot(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots WHERE database = current_database()"); err != nil {
		t.Fatal(err)
	}
	w := queue.NewWorker(db, queuetest.Config(t), slog.New(slog.DiscardHandler))
	err := w.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "run the queue migrations") {
		t.Fatalf("err %v", err)
	}
}

func TestSlotName(t *testing.T) {
	t.Parallel()
	db := openDB(t)
	var database string
	if err := db.NewRaw("SELECT current_database()").Scan(context.Background(), &database); err != nil {
		t.Fatal(err)
	}
	var n int
	err := db.NewRaw("SELECT count(*) FROM pg_replication_slots WHERE slot_name = ? AND plugin = 'pgoutput' AND failover",
		queue.SlotName(database)).Scan(context.Background(), &n)
	if err != nil || n != 1 {
		t.Fatalf("slot %s: n=%d err=%v", queue.SlotName(database), n, err)
	}
}

// slotActive 报告 db 的复制槽是否正被某个 Worker 使用。
func slotActive(t *testing.T, db bun.IDB) bool {
	t.Helper()
	var active bool
	if err := db.NewRaw("SELECT active FROM pg_replication_slots WHERE database = current_database()").Scan(context.Background(), &active); err != nil {
		t.Fatal(err)
	}
	return active
}
