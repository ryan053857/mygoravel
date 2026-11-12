package operation_test

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
)

var (
	alice = identity.Subject{ID: "u1", Type: "user", Tenant: "acme"}
	bob   = identity.Subject{ID: "u2", Type: "user", Tenant: "acme"}
)

var errBadFile = errorx.Define(errorx.InvalidArgument, "test.bad_file")

// migrations 合并队列和操作的迁移。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": queue.Migrations(), "2": operation.Migrations()} {
		err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src, p)
			dir, name := path.Split(p)
			merged[dir+prefix+name] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return merged
}

type env struct {
	db  *bun.DB
	ops *operation.Service
}

func newEnv(t *testing.T) env {
	t.Helper()
	db := dbtest.Open(t, migrations(t))
	return env{db: db, ops: operation.New(db)}
}

func (e env) start(t *testing.T, owner identity.Subject, kind string) *operation.Operation {
	t.Helper()
	op, err := e.ops.Start(context.Background(), e.db, owner, kind)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

func (e env) get(t *testing.T, id uuid.UUID) *operation.Operation {
	t.Helper()
	op, err := e.ops.Get(context.Background(), alice, id)
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// recorder 记录每次保存时 saved 收到的操作。
type recorder struct {
	mu    sync.Mutex
	saves []operation.Operation
}

func (r *recorder) saved(_ context.Context, _ database.Tx, op *operation.Operation) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.saves = append(r.saves, *op)
	return nil
}

// progress 返回每次保存时的状态和进度，例如 running:3/5-1 表示处理了 3 个、预计 5 个、失败 1 个。
func (r *recorder) progress() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := make([]string, len(r.saves))
	for i, op := range r.saves {
		list[i] = fmt.Sprintf("%s:%d/%d-%d", op.Status, op.Processed, op.Total, op.Failed)
	}
	return list
}

func TestStartGetList(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	if _, err := e.ops.Start(ctx, e.db, alice, ""); err == nil {
		t.Fatal("empty kind accepted")
	}
	imp := e.start(t, alice, "articles.import")
	exp := e.start(t, alice, "articles.export")
	other := e.start(t, bob, "articles.import")
	tenant := e.start(t, identity.Subject{ID: alice.ID, Type: alice.Type, Tenant: "globex"}, "articles.import")

	got := e.get(t, imp.ID)
	if got.Status != operation.StatusPending || got.Kind != "articles.import" || got.Owner().ID != alice.ID || got.Owner().Tenant != alice.Tenant || got.Finished() {
		t.Fatalf("get: %+v", got)
	}
	for _, id := range []uuid.UUID{other.ID, tenant.ID, uuid.Must(uuid.NewV7())} {
		if _, err := e.ops.Get(ctx, alice, id); !errors.Is(err, operation.ErrNotFound) {
			t.Fatalf("get %s: %v", id, err)
		}
	}

	ids := func(list []operation.Operation, err error) []uuid.UUID {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		out := []uuid.UUID{}
		for _, op := range list {
			out = append(out, op.ID)
		}
		return out
	}
	if got := ids(e.ops.List(ctx, alice, operation.Query{})); !slices.Equal(got, []uuid.UUID{exp.ID, imp.ID}) {
		t.Fatalf("list: %v", got)
	}
	if got := ids(e.ops.List(ctx, alice, operation.Query{Kind: "articles.import"})); !slices.Equal(got, []uuid.UUID{imp.ID}) {
		t.Fatalf("list kind: %v", got)
	}
	if got := ids(e.ops.List(ctx, alice, operation.Query{Limit: 1})); !slices.Equal(got, []uuid.UUID{exp.ID}) {
		t.Fatalf("list limit: %v", got)
	}
	if got := ids(e.ops.List(ctx, alice, operation.Query{Before: exp.ID})); !slices.Equal(got, []uuid.UUID{imp.ID}) {
		t.Fatalf("list before: %v", got)
	}
}

func TestRun(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.reindex")
	rec := &recorder{}
	var written []string
	err := e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		if got := r.Operation(); got.Status != operation.StatusRunning || got.Owner().ID != alice.ID || got.Owner().Tenant != alice.Tenant {
			t.Errorf("operation in run: %+v", got)
		}
		r.SetTotal(5)
		r.Add(2, 1)
		if err := r.Save(ctx, func(ctx context.Context, tx database.Tx) error {
			written = append(written, "first")
			return nil
		}); err != nil {
			return err
		}
		if got := r.Operation(); got.Processed != 2 || got.Failed != 1 || got.Total != 5 {
			t.Errorf("saved operation: %+v", got)
		}
		r.Add(3, 0) // 由结束时的保存一起保存
		return nil
	}, rec.saved)
	if err != nil {
		t.Fatal(err)
	}
	if p := rec.progress(); !slices.Equal(p, []string{"running:0/0-0", "running:2/5-1", "completed:5/5-1"}) {
		t.Fatalf("saves: %v", p)
	}
	if !slices.Equal(written, []string{"first"}) {
		t.Fatalf("written: %v", written)
	}
	got := e.get(t, op.ID)
	if got.Status != operation.StatusCompleted || got.Processed != 5 || got.Failed != 1 || got.CompletedAt == nil || !got.Finished() {
		t.Fatalf("completed: %+v", got)
	}

	// 已经结束的操作不再执行。
	if err := e.ops.Run(ctx, op.ID, func(context.Context, *operation.Run) error {
		t.Error("finished operation ran again")
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
	// 不存在的操作（例如已被清理）什么也不做。
	if err := e.ops.Run(ctx, uuid.Must(uuid.NewV7()), func(context.Context, *operation.Run) error {
		t.Error("missing operation ran")
		return nil
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestRunRetryResumesFromSavedProgress(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.reindex")
	rec := &recorder{}
	down := errors.New("database down")
	err := e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		r.SetTotal(4)
		r.Add(2, 0)
		if err := r.Save(ctx, nil); err != nil {
			return err
		}
		r.Add(1, 1) // 没有保存，重试时丢弃
		return down
	}, rec.saved)
	if !errors.Is(err, down) {
		t.Fatalf("run: %v", err)
	}
	if got := e.get(t, op.ID); got.Status != operation.StatusRunning || got.Processed != 2 {
		t.Fatalf("after error: %+v", got)
	}
	err = e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		if got := r.Operation(); got.Processed != 2 || got.Total != 4 {
			t.Errorf("resumed from: %+v", got)
		}
		r.Add(2, 0)
		return nil
	}, rec.saved)
	if err != nil {
		t.Fatal(err)
	}
	if p := rec.progress(); !slices.Equal(p, []string{"running:0/0-0", "running:2/4-0", "completed:4/4-0"}) {
		t.Fatalf("saves: %v", p)
	}
}

func TestRunRestart(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.export")
	rec := &recorder{}
	fail := true
	for range 2 {
		_ = e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
			r.Restart()
			r.SetTotal(3)
			r.Add(2, 0)
			if err := r.Save(ctx, nil); err != nil {
				return err
			}
			if fail {
				fail = false
				return errors.New("disk full")
			}
			r.Add(1, 0)
			return nil
		}, rec.saved)
	}
	want := []string{"running:0/0-0", "running:2/3-0", "running:2/3-0", "completed:3/3-0"}
	if p := rec.progress(); !slices.Equal(p, want) {
		t.Fatalf("saves: %v, want %v", p, want)
	}
}

func TestComplete(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.export")
	rec := &recorder{}
	var written int
	err := e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		r.SetTotal(2)
		r.Add(2, 0)
		if err := r.Complete(ctx, func(context.Context, database.Tx) error {
			written++
			return nil
		}); err != nil {
			return err
		}
		if err := r.Save(ctx, nil); err == nil {
			t.Error("saved after complete")
		}
		return errors.New("late error") // 结果已经保存，不再变为 failed
	}, rec.saved)
	if err == nil {
		t.Fatal("late error dropped")
	}
	if p := rec.progress(); !slices.Equal(p, []string{"running:0/0-0", "completed:2/2-0"}) || written != 1 {
		t.Fatalf("saves: %v written %d", p, written)
	}
	if got := e.get(t, op.ID); got.Status != operation.StatusCompleted {
		t.Fatalf("status: %+v", got)
	}
}

func TestRunBusinessErrorFails(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.import")
	rec := &recorder{}
	err := e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		r.Add(1, 0) // 没有保存
		return errBadFile.New(errorx.Public("name", "a.xlsx"))
	}, rec.saved)
	if err != nil {
		t.Fatalf("business error returned to queue: %v", err)
	}
	got := e.get(t, op.ID)
	if got.Status != operation.StatusFailed || got.Error != "test.bad_file" || got.ErrorData["name"] != "a.xlsx" || got.Processed != 0 || got.CompletedAt == nil {
		t.Fatalf("failed: %+v", got)
	}
	if p := rec.progress(); !slices.Equal(p, []string{"running:0/0-0", "failed:0/0-0"}) {
		t.Fatalf("saves: %v", p)
	}
}

func TestSaveRollsBackWithWrite(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.import")
	boom := errors.New("write failed")
	err := e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		r.Add(3, 0)
		return r.Save(ctx, func(context.Context, database.Tx) error { return boom })
	}, nil)
	if !errors.Is(err, boom) {
		t.Fatalf("run: %v", err)
	}
	if got := e.get(t, op.ID); got.Processed != 0 || got.Status != operation.StatusRunning {
		t.Fatalf("progress saved without write: %+v", got)
	}
}

func TestSaveDetectsAnotherRun(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	op := e.start(t, alice, "articles.import")
	err := e.ops.Run(ctx, op.ID, func(ctx context.Context, r *operation.Run) error {
		// 另一次执行（例如租约过期后被重新领取的任务）保存了进度。
		stop := errors.New("stop")
		err := e.ops.Run(ctx, op.ID, func(ctx context.Context, other *operation.Run) error {
			other.Add(5, 0)
			if err := other.Save(ctx, nil); err != nil {
				return err
			}
			return stop
		}, nil)
		if !errors.Is(err, stop) {
			t.Errorf("other run: %v", err)
		}
		r.Add(1, 0)
		return r.Save(ctx, nil)
	}, nil)
	if err == nil {
		t.Fatal("stale run saved")
	}
	if got := e.get(t, op.ID); got.Processed != 5 || got.Status != operation.StatusRunning {
		t.Fatalf("progress: %+v", got)
	}
}

// runQueue 在最多执行两次的队列中执行 fn，等待操作变为 failed。
func runQueue(t *testing.T, e env, fn func(ctx context.Context, id uuid.UUID) error) {
	t.Helper()
	topic := queue.Topic[uuid.UUID]("operation.test")
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	cfg.MaxAttempts = 2
	w := queue.NewWorker(queue.Database(e.db), cfg, slog.New(slog.DiscardHandler))
	topic.Handle(w, fn)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
	})
	op := e.start(t, alice, "articles.import")
	if err := topic.Publish(context.Background(), queue.Database(e.db), op.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		got := e.get(t, op.ID)
		if got.Finished() {
			if got.Status != operation.StatusFailed || got.Error != "operation.failed" {
				t.Fatalf("finished: %+v", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %+v did not fail", got)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFinalAttemptFailsOperation(t *testing.T) {
	e := newEnv(t)
	var mu sync.Mutex
	attempts := 0
	runQueue(t, e, func(ctx context.Context, id uuid.UUID) error {
		return e.ops.Run(ctx, id, func(context.Context, *operation.Run) error {
			mu.Lock()
			attempts++
			mu.Unlock()
			return queue.RetryAfter(errors.New("always down"), 0)
		}, nil)
	})
	mu.Lock()
	defer mu.Unlock()
	if attempts != 2 {
		t.Fatalf("attempts: %d", attempts)
	}
}

func TestPermanentErrorFailsOperation(t *testing.T) {
	e := newEnv(t)
	runQueue(t, e, func(ctx context.Context, id uuid.UUID) error {
		return e.ops.Run(ctx, id, func(context.Context, *operation.Run) error {
			return queue.Permanent(errors.New("payload refers to a deleted article"))
		}, nil)
	})
}

func TestCleanup(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	old := e.start(t, alice, "articles.import")
	recent := e.start(t, alice, "articles.import")
	if _, err := e.db.NewUpdate().Model((*operation.Operation)(nil)).Set("created_at = ?", time.Now().Add(-48*time.Hour).UTC()).
		Where("id = ?", old.ID).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	n, err := e.ops.Cleanup(24*time.Hour)(ctx, 10)
	if err != nil || n != 1 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	if _, err := e.ops.Get(ctx, alice, old.ID); !errors.Is(err, operation.ErrNotFound) {
		t.Fatalf("old: %v", err)
	}
	e.get(t, recent.ID)
}
