package operation_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
)

var unitTopic = queue.Topic[operation.Unit[string]]("operation.unit")

// worker 用数据库队列处理 unitTopic，最多执行 maxAttempts 次，失败后立即重试。
func worker(t *testing.T, e env, maxAttempts int, handle func(ctx context.Context, u operation.Unit[string]) error) {
	t.Helper()
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	cfg.MaxAttempts = maxAttempts
	logger := slog.New(slog.DiscardHandler)
	r := queue.NewRouter(logger)
	sub := queue.NewSubscriber(queue.Database(e.db), cfg, logger)
	unitTopic.Handle(r, sub, handle)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.Run(ctx) }()
	<-r.Running()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_ = sub.Close()
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
		return operation.Fanout(ctx, tx, queue.Database(tx), op.ID, unitTopic, items...)
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
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		e := newEnv(t, driver)
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
	})
}

func TestHandleUnitSkipsSettledUnits(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		e := newEnv(t, driver)
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
		if err := operation.Fanout(ctx, e.db, queue.Database(e.db), op.ID, unitTopic, "late"); err == nil {
			t.Fatal("fanout to finished operation")
		}
		if err := operation.Fanout(ctx, e.db, queue.Database(e.db), op.ID, unitTopic); err == nil {
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
	})
}

// claimAll 领取 unitTopic 中全部到期的任务，解码到 units。
func (e env) claimAll(t *testing.T, units *[]operation.Unit[string]) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	claims, err := queue.Database(e.db).Claim(ctx, queue.ClaimRequest{Topic: string(unitTopic), Max: 100, Lease: time.Minute, PollInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	*units = (*units)[:0]
	for _, c := range claims {
		var u operation.Unit[string]
		if err := json.Unmarshal(c.Job().Payload, &u); err != nil {
			t.Fatal(err)
		}
		*units = append(*units, u)
		if err := c.Ack(ctx); err != nil {
			t.Fatal(err)
		}
	}
	slices.SortFunc(*units, func(a, b operation.Unit[string]) int { return strings.Compare(a.Data, b.Data) })
}

func TestRetryFailedUnit(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		e := newEnv(t, driver)
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
		failed := queue.NewFailedJobs(e.db)
		jobs, err := failed.List(ctx, queue.FailedQuery{Topic: string(unitTopic)})
		if err != nil || len(jobs) != 1 {
			t.Fatalf("failed jobs: %+v %v", jobs, err)
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
	})
}
