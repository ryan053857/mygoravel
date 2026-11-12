package vector_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
	"github.com/ryan053857/mygoravel/search"
	"github.com/ryan053857/mygoravel/vector"
)

var hybridChunks = vector.Define[chunkDoc]("hybrid-chunks", vector.Hybrid(search.Bigram))

func (f *fixture) putHybrid(t *testing.T, key string, docs ...chunkDoc) {
	t.Helper()
	f.src.set(key, docs...)
	if err := hybridChunks.Refresh(context.Background(), f.store, f.src.load, key); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) searchHybrid(t *testing.T, q vector.Query) []vector.Hit[chunkDoc] {
	t.Helper()
	hits, err := hybridChunks.Search(context.Background(), f.store, q)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func TestHybridKeywords(t *testing.T) {
	f := setup(t)
	f.putHybrid(t, "a", chunkDoc{File: "a", Text: "型号 X-2000 的保修期是两年"})
	f.putHybrid(t, "b", chunkDoc{File: "b", Text: "退货需要在七天内申请"}, chunkDoc{File: "b", Text: "发票随货寄出"})
	f.putHybrid(t, "c", chunkDoc{File: "c", Text: "售后服务热线全天开放"})

	// aitest.Embedder 给不同的文本几乎正交的向量，语义那一路的排序是随机的：
	// 只有关键词命中的分段在两路中都有排名，总是排在第一
	for _, q := range []string{"X-2000", "x-2000 保修", "保修期多久"} {
		hits := f.searchHybrid(t, vector.Query{Text: q, Limit: 1})
		if len(hits) != 1 || hits[0].Key != "a" {
			t.Fatalf("%q: hits = %+v", q, hits)
		}
	}
	// 没有切分出词的查询只走语义那一路，结果仍然完整
	if hits := f.searchHybrid(t, vector.Query{Text: "！？"}); len(hits) != 4 {
		t.Fatalf("query without terms returned %d hits", len(hits))
	}
	// 原文完全相同的查询在两路中都排第一
	if hits := f.searchHybrid(t, vector.Query{Text: "发票随货寄出"}); hits[0].Doc.Text != "发票随货寄出" {
		t.Fatalf("exact text hits = %v", texts(hits))
	}
	// 过滤对两路都生效
	hits := f.searchHybrid(t, vector.Query{Text: "X-2000", Filter: vector.Eq("file", "b")})
	if got := sorted(texts(hits)); !slices.Equal(got, []string{"发票随货寄出", "退货需要在七天内申请"}) {
		t.Fatalf("filtered hits = %v", got)
	}
}

func TestHybridReusesVectors(t *testing.T) {
	f := setup(t)
	f.putHybrid(t, "a", chunkDoc{Text: "一段文字"}, chunkDoc{Text: "另一段"})
	before := len(f.emb.Requests())
	f.putHybrid(t, "a", chunkDoc{Text: "另一段"}, chunkDoc{Text: "一段文字"}, chunkDoc{Text: "新的一段"})
	if got := f.emb.Requests(); len(got) != before+1 || !slices.Equal(got[before], []string{"新的一段"}) {
		t.Fatalf("embedding requests = %v", got)
	}
	if got := sorted(texts(f.searchHybrid(t, vector.Query{Text: "一段"}))); len(got) != 3 {
		t.Fatalf("documents = %v", got)
	}
}

func TestHybridReindex(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	plain := vector.Define[chunkDoc]("mixed")
	hybrid := vector.Define[chunkDoc]("mixed", vector.Hybrid(search.Bigram))
	f.src.set("a", chunkDoc{Text: "型号 X-2000 的保修期是两年"})
	f.src.set("b", chunkDoc{Text: "退货需要在七天内申请"})
	if err := plain.Refresh(ctx, f.store, f.src.load, "a", "b"); err != nil {
		t.Fatal(err)
	}

	// 加上 Hybrid 之后、重建完成之前，原来的版本照常写入和查询（只有语义那一路）
	f.src.set("c", chunkDoc{Text: "售后服务热线全天开放"})
	if err := hybrid.Refresh(ctx, f.store, f.src.load, "c"); err != nil {
		t.Fatal(err)
	}
	if hits, err := hybrid.Search(ctx, f.store, vector.Query{Text: "售后服务热线全天开放"}); err != nil || len(hits) != 3 || hits[0].Key != "c" {
		t.Fatalf("search before reindex = %+v, %v", hits, err)
	}

	var op *operation.Operation
	err := database.RunInTx(ctx, f.db, nil, func(ctx context.Context, tx database.Tx) error {
		var err error
		op, err = hybrid.Reindex(ctx, tx, operation.New(f.db), system)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	w := queue.NewWorker(f.db, queuetest.Config(t), nil)
	hybrid.Handle(w, f.store, f.src.load, f.src.scan)
	queuetest.Run(t, w)
	if op := waitOperation(t, f.db, op.ID); op.Status != operation.StatusCompleted || op.Processed != 3 {
		t.Fatalf("operation = %+v", op)
	}
	for _, q := range []string{"X-2000", "七天"} {
		hits, err := hybrid.Search(ctx, f.store, vector.Query{Text: q, Limit: 1})
		want := map[string]string{"X-2000": "a", "七天": "b"}[q]
		if err != nil || len(hits) != 1 || hits[0].Key != want {
			t.Fatalf("%q after reindex = %+v, %v", q, hits, err)
		}
	}
}
