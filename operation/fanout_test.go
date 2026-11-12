package operation_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
)

var unitTopic = queue.Topic[operation.Unit[string]]("operation.unit")

// worker 用数据库队列处理 unitTopic，最多执行 maxAttempts 次，失败后立即重试。
func worker(t *testing.T, e env, maxAttempts int, handle func(ctx context.Context, u operation.Unit[string]) error) {
	t.Helper()
	cfg := queuetest.Config(t)
	cfg.MaxAttempts = maxAttempts
	w := queue.NewWorker(e.db, cfg, slog.New(slog.DiscardHandler))
	unitTopic.Handle(w, handle)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
}

// fanout 登记一个操作，在同一个事务中把 items 扇出到 unitTopic。
func (e env) fanout(t *testing.T, items ...string) *operation.Operation {
	t.Helper()
	var op *operation.Operation
	err := database.RunInTx(context.Background(), e.db, nil, func(ctx context.Context, tx database.Tx) error {
		var err error
		if op, err = e.ops.Start(ctx, tx, alice, "articles.summarize"); err != nil {
			return err
		}
		return operation.Fanout(ctx, tx, op.ID, unitTopic, items...)
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// wait 等待操作满足 done。
func (e env) wait(t *testing.T, id uuid.UUID, done func(op *operation.Operation) bool) *operation.Operation {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		op := e.get(t, id)
		if done(op) {
			return op
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation stuck: %+v", op)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFanout(t *testing.T) {
	e := newEnv(t)
	rec := &recorder{}
	var mu sync.Mutex
	seen := map[string]int{}
	worker(t, e, 3, operation.HandleUnit(e.ops, func(ctx context.Context, op *operation.Operation, item string) error {
		mu.Lock()
		seen[item]++
		mu.Unlock()
		if op.Owner().ID != alice.ID || op.Kind != "articles.summarize" {
			return errors.Newf("wrong operation %+v", op)
		}
		if item == "gone" {
			return errBadFile.New() // 业务错误：这个单位失败，不再重试
		}
		return nil
	}, rec.saved))

	op := e.fanout(t, "a", "b", "c", "gone")
	if got := e.get(t, op.ID); got.Total != 4 || got.Status == operation.StatusPending {
		t.Fatalf("after fanout: %+v", got)
	}
	got := e.wait(t, op.ID, (*operation.Operation).Finished)
	if got.Status != operation.StatusCompleted || got.Processed != 4 || got.Failed != 1 || got.CompletedAt == nil {
		t.Fatalf("completed: %+v", got)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, item := range []string{"a", "b", "c", "gone"} {
		if seen[item] != 1 {
			t.Fatalf("seen: %v", seen)
		}
	}
	p := rec.progress()
	if len(p) != 4 || p[3] != "completed:4/4-1" {
		t.Fatalf("saves: %v", p)
	}
}

func TestHandleUnitSkipsSettledUnits(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var calls []string
	handle := operation.HandleUnit(e.ops, func(_ context.Context, _ *operation.Operation, item string) error {
		calls = append(calls, item)
		return nil
	}, nil)
	op := e.fanout(t, "a", "b")
	var units []operation.Unit[string]
	e.claimAll(t, &units)
	if len(units) != 2 {
		t.Fatalf("units: %+v", units)
	}
	// 第一个单位投递两次（例如确认之前进程崩溃），第二次不再执行。
	for _, u := range []operation.Unit[string]{units[0], units[0], units[1]} {
		if err := handle(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(calls, []string{units[0].Data, units[1].Data}) {
		t.Fatalf("calls: %v", calls)
	}
	if got := e.get(t, op.ID); got.Status != operation.StatusCompleted || got.Processed != 2 {
		t.Fatalf("completed: %+v", got)
	}
	// 已经结束的操作不能再加入单位。
	if err := operation.Fanout(ctx, e.db, op.ID, unitTopic, "late"); err == nil {
		t.Fatal("fanout to finished operation")
	}
	if err := operation.Fanout(ctx, e.db, op.ID, unitTopic); err == nil {
		t.Fatal("fanout without items")
	}
	// 操作被删除后，剩下的任务什么也不做。
	other := e.fanout(t, "x")
	e.claimAll(t, &units)
	if _, err := e.db.NewDelete().Model((*operation.Operation)(nil)).Where("id = ?", other.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := handle(ctx, units[0]); err != nil || len(calls) != 2 {
		t.Fatalf("deleted operation: %v %v", err, calls)
	}
}

// claimAll 取出 outbox 中 unitTopic 的全部任务，解码到 units，不经过 NSQ。
func (e env) claimAll(t *testing.T, units *[]operation.Unit[string]) {
	t.Helper()
	ctx := context.Background()
	var bodies [][]byte
	err := e.db.NewSelect().Table("queue_outbox").Column("body").Where("topic = ?", string(unitTopic)).Order("id").Scan(ctx, &bodies)
	if err != nil {
		t.Fatal(err)
	}
	*units = (*units)[:0]
	for _, body := range bodies {
		var msg struct {
			Payload operation.Unit[string] `json:"payload"`
		}
		if err := json.Unmarshal(body, &msg); err != nil {
			t.Fatal(err)
		}
		*units = append(*units, msg.Payload)
	}
	if _, err := e.db.NewDelete().Table("queue_outbox").Where("topic = ?", string(unitTopic)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRetryFailedUnit(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	rec := &recorder{}
	var mu sync.Mutex
	down := true
	worker(t, e, 2, operation.HandleUnit(e.ops, func(_ context.Context, _ *operation.Operation, item string) error {
		mu.Lock()
		defer mu.Unlock()
		if item == "flaky" && down {
			return queue.RetryAfter(errors.New("service down"), 0)
		}
		return nil
	}, rec.saved))

	op := e.fanout(t, "ok", "flaky")
	got := e.wait(t, op.ID, (*operation.Operation).Finished)
	if got.Status != operation.StatusCompleted || got.Processed != 2 || got.Failed != 1 {
		t.Fatalf("completed with failure: %+v", got)
	}
	saves := len(rec.progress())

	// 服务恢复后重试失败任务，计数随之修正，但不再调用 saved。
	mu.Lock()
	down = false
	mu.Unlock()
	// 单位先在 settle 中记为失败、操作随之结束，处理函数返回之后队列才把任务移进失败任务表，
	// 所以操作结束时失败任务可能还没出现。
	failed := queue.NewFailedJobs(e.db)
	var jobs []queue.FailedJob
	for deadline := time.Now().Add(10 * time.Second); len(jobs) == 0; time.Sleep(20 * time.Millisecond) {
		var err error
		if jobs, err = failed.List(ctx, queue.FailedQuery{Topic: string(unitTopic)}); err != nil {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("failed job never appeared")
		}
	}
	if len(jobs) != 1 {
		t.Fatalf("failed jobs: %+v", jobs)
	}
	if err := failed.Retry(ctx, jobs[0].ID); err != nil {
		t.Fatal(err)
	}
	got = e.wait(t, op.ID, func(op *operation.Operation) bool { return op.Failed == 0 })
	if got.Status != operation.StatusCompleted || got.Processed != 2 {
		t.Fatalf("after retry: %+v", got)
	}
	if p := rec.progress(); len(p) != saves {
		t.Fatalf("saved after finish: %v", p)
	}
}
