package vector_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/ai/aitest"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
	"github.com/ryan053857/mygoravel/search/searchcmd"
	"github.com/ryan053857/mygoravel/vector"
)

var system = identity.Subject{Type: identity.System, ID: "test"}

// 向量索引和全文搜索索引共用 search reindex 命令。
var _ searchcmd.Index = chunks

// startReindex 在一个事务中发起 chunks 的重建，返回操作的 ID。
func startReindex(t *testing.T, db *bun.DB) uuid.UUID {
	t.Helper()
	var op *operation.Operation
	err := database.RunInTx(context.Background(), db, nil, func(ctx context.Context, tx database.Tx) error {
		var err error
		op, err = chunks.Reindex(ctx, tx, operation.New(db), system)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != operation.StatusPending || op.Kind != "vector.reindex.chunks" {
		t.Fatalf("operation = %+v", op)
	}
	return op.ID
}

// runWorker 在后台运行处理 chunks 任务的 worker，任务用 s 同步。
func runWorker(t *testing.T, db *bun.DB, s *vector.Store, src *source) {
	t.Helper()
	w := queue.NewWorker(db, queuetest.Config(t), nil)
	chunks.Handle(w, s, src.load, src.scan)
	queuetest.Run(t, w)
}

// eventually 每隔一小段时间检查 cond，10 秒内不成立时失败。
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitOperation(t *testing.T, db *bun.DB, id uuid.UUID) *operation.Operation {
	t.Helper()
	var op *operation.Operation
	eventually(t, "operation to finish", func() bool {
		var err error
		if op, err = operation.New(db).Get(context.Background(), system, id); err != nil {
			t.Fatal(err)
		}
		return op.Finished()
	})
	return op
}

func version(t *testing.T, db bun.IDB) int {
	t.Helper()
	var v []int
	if err := db.NewRaw("SELECT version FROM vector_indexes WHERE name = 'chunks'").Scan(context.Background(), &v); err != nil {
		t.Fatal(err)
	}
	if len(v) == 0 {
		return 1
	}
	return v[0]
}

func TestReindexSwitchesModel(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t)
	shared := newShared(t, f.db)
	oldEmb, newEmb := aitest.NewEmbedder(dims), aitest.NewEmbedder(dims/2)
	oldModel := vector.Model{Name: "old", Dims: dims, Embedder: oldEmb}
	old := shared.store(oldModel)
	for _, key := range []string{"a", "b", "c"} {
		f.src.set(key, chunkDoc{Text: "原来的" + key})
		if err := chunks.Refresh(ctx, old, f.src.load, key); err != nil {
			t.Fatal(err)
		}
	}

	// 换了模型：新版本用 new 建立，重建完成之前查询和同步当前版本仍用 old
	changed := shared.store(vector.Model{Name: "new", Dims: dims / 2, Embedder: newEmb}, vector.WithPrevious(oldModel))
	if hits, err := chunks.Search(ctx, changed, vector.Query{Text: "原来的a"}); err != nil || len(hits) != 3 || hits[0].Key != "a" {
		t.Fatalf("search before reindex = %+v, %v", hits, err)
	}
	if n := len(newEmb.Requests()); n != 0 {
		t.Fatalf("new model used before reindex: %v", newEmb.Requests())
	}

	id := startReindex(t, f.db)
	// 重建登记之后的修改同时写入两个版本，各用自己的模型
	f.src.set("a", chunkDoc{Text: "修改后的a"})
	if err := chunks.Refresh(ctx, changed, f.src.load, "a"); err != nil {
		t.Fatal(err)
	}
	if got := newEmb.Requests(); len(got) != 1 || !slices.Equal(got[0], []string{"修改后的a"}) {
		t.Fatalf("new model requests = %v", got)
	}
	f.src.set("d", chunkDoc{Text: "没有同步过的d"}) // 绕过 Sync 的修改，重建后补上
	f.src.set("c")                            // 已删除，重建后的版本中没有它

	runWorker(t, f.db, changed, f.src)
	op := waitOperation(t, f.db, id)
	if op.Status != operation.StatusCompleted || op.Processed != 4 || op.Failed != 0 {
		t.Fatalf("operation = %+v", op)
	}
	if v := version(t, f.db); v != 2 {
		t.Fatalf("version = %d, want 2", v)
	}
	hits, err := chunks.Search(ctx, changed, vector.Query{Text: "没有同步过的d"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sorted(texts(hits)); !slices.Equal(got, []string{"修改后的a", "原来的b", "没有同步过的d"}) || hits[0].Key != "d" {
		t.Fatalf("hits after reindex = %+v", hits)
	}

	// 切换之后不再需要旧模型；旧版本的 collection 已经删除
	only := shared.store(vector.Model{Name: "new", Dims: dims / 2, Embedder: newEmb})
	if _, err := chunks.Search(ctx, only, vector.Query{Text: "原来的b"}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "old version to be dropped", func() bool { return !shared.exists(t, "chunks_v1") })
	if !shared.exists(t, "chunks_v2") {
		t.Fatal("version 2 is missing")
	}
	if _, err := chunks.Search(ctx, old, vector.Query{Text: "x"}); err == nil {
		t.Fatal("search with only the old model succeeded after the switch")
	}
}

func TestReindexSuperseded(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.put(t, "a", chunkDoc{Text: "一"})
	f.put(t, "b", chunkDoc{Text: "二"})
	first := startReindex(t, f.db)
	second := startReindex(t, f.db)
	runWorker(t, f.db, f.store, f.src)

	if op := waitOperation(t, f.db, first); op.Status != operation.StatusFailed || op.Error != "vector.reindex_superseded" {
		t.Fatalf("first = %+v, want failed as superseded", op)
	}
	if op := waitOperation(t, f.db, second); op.Status != operation.StatusCompleted || op.Processed != 2 {
		t.Fatalf("second = %+v, want completed", op)
	}
	if v := version(t, f.db); v != 3 {
		t.Fatalf("version = %d, want 3", v)
	}
	if got := sorted(texts(f.search(t, vector.Query{Text: "一"}))); !slices.Equal(got, []string{"一", "二"}) {
		t.Fatalf("documents = %v", got)
	}
}

func TestReindexInvalidDocument(t *testing.T) {
	t.Parallel()
	f := setup(t)
	f.put(t, "a", chunkDoc{Text: "一"})
	f.src.set("bad", chunkDoc{File: "没有文本"})
	id := startReindex(t, f.db)
	runWorker(t, f.db, f.store, f.src)
	if op := waitOperation(t, f.db, id); op.Status != operation.StatusCompleted || op.Processed != 2 || op.Failed != 1 {
		t.Fatalf("operation = %+v, want completed with 1 failed", op)
	}
	if got := texts(f.search(t, vector.Query{Text: "一"})); !slices.Equal(got, []string{"一"}) {
		t.Fatalf("documents = %v", got)
	}
}

func TestReindexChangesDimensions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := setup(t)
	shared := newShared(t, f.db)
	full, reduced := aitest.NewEmbedder(dims), aitest.NewEmbedder(dims/2)
	fullModel := vector.Model{Name: "embedding", Dims: dims, Embedder: full}
	f.src.set("a", chunkDoc{Text: "原来的a"})
	if err := chunks.Refresh(ctx, shared.store(fullModel), f.src.load, "a"); err != nil {
		t.Fatal(err)
	}

	// 同一个模型改用更少的维度：名称相同、维度不同的旧模型用 WithPrevious 传入
	changed := shared.store(vector.Model{Name: "embedding", Dims: dims / 2, Embedder: reduced}, vector.WithPrevious(fullModel))
	if hits, err := chunks.Search(ctx, changed, vector.Query{Text: "原来的a"}); err != nil || len(hits) != 1 {
		t.Fatalf("search before reindex = %+v, %v", hits, err)
	}
	id := startReindex(t, f.db)
	f.src.set("b", chunkDoc{Text: "重建期间的b"})
	if err := chunks.Refresh(ctx, changed, f.src.load, "b"); err != nil {
		t.Fatal(err)
	}
	runWorker(t, f.db, changed, f.src)
	if op := waitOperation(t, f.db, id); op.Status != operation.StatusCompleted || op.Processed != 2 {
		t.Fatalf("operation = %+v", op)
	}
	only := shared.store(vector.Model{Name: "embedding", Dims: dims / 2, Embedder: reduced})
	hits, err := chunks.Search(ctx, only, vector.Query{Text: "重建期间的b"})
	if err != nil || len(hits) != 2 || hits[0].Key != "b" {
		t.Fatalf("search after reindex = %+v, %v", hits, err)
	}
}
