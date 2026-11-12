package search_test

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/page"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/search"
)

// articleDocV2 是修改之后的文档类型：去掉了租户和部分过滤字段，加了 lang。
type articleDocV2 struct {
	ID    string `json:"id"`
	Title string `json:"title" search:"text,boost"`
	Body  string `json:"body" search:"text"`
	Lang  string `json:"lang" search:"filter"`
}

var articlesV2 = search.Define[articleDocV2]("articles")

var system = identity.Subject{Type: identity.System, ID: "test"}

// loadV2 把 src 中的文档转换为 articleDocV2；Status 为 bad 的文档无法建索引。
func loadV2(src *source) search.LoadFunc[articleDocV2] {
	return func(ctx context.Context, tx bun.IDB, key string) ([]articleDocV2, error) {
		docs, err := src.load(ctx, tx, key)
		out := make([]articleDocV2, len(docs))
		for i, d := range docs {
			out[i] = articleDocV2{ID: d.ID, Title: d.Title, Body: d.Body, Lang: "zh"}
			if d.Status == "bad" {
				out[i].Lang = strings.Repeat("x", 192)
			}
		}
		return out, err
	}
}

func startReindex(t *testing.T, db *bun.DB) uuid.UUID {
	t.Helper()
	var op *operation.Operation
	err := database.RunInTx(context.Background(), db, nil, func(ctx context.Context, tx database.Tx) error {
		var err error
		op, err = articlesV2.Reindex(ctx, tx, operation.New(db), system)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != operation.StatusPending || op.Kind != "search.reindex.articles" {
		t.Fatalf("operation = %+v", op)
	}
	return op.ID
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

func versions(t *testing.T, db bun.IDB) []int {
	t.Helper()
	var out []int
	if err := db.NewRaw("SELECT DISTINCT version FROM search_entries ORDER BY version").Scan(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func findV2(t *testing.T, db bun.IDB, q search.Query) []string {
	t.Helper()
	res, err := articlesV2.Search(context.Background(), db, q)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(res.Items))
	for i, h := range res.Items {
		out[i] = h.Key
	}
	return sorted(out)
}

func TestReindex(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db, src := fixture(t, driver,
			articleDoc{ID: "1", Title: "全文搜索"},
			articleDoc{ID: "2", Title: "原来的标题"},
			articleDoc{ID: "3", Title: "要删除的文章"},
		)
		load := loadV2(src)
		if got := findV2(t, db, search.Query{Filter: search.Eq("lang", "zh")}); len(got) != 0 {
			t.Fatalf("new field matched before reindex: %v", got)
		}

		id := startReindex(t, db)
		// 重建期间的同步同时写入两个版本，查询仍然使用原来的版本
		src.set("2", articleDoc{ID: "2", Title: "修改后的标题"})
		src.set("3")
		if err := articlesV2.Refresh(ctx, db, load, "2", "3"); err != nil {
			t.Fatal(err)
		}
		if got := versions(t, db); !slices.Equal(got, []int{1, 2}) {
			t.Fatalf("versions = %v, want [1 2]", got)
		}
		expectKeys(t, find(t, db, search.Query{Text: "修改后"}), "2")
		expectKeys(t, find(t, db, search.Query{}), "1", "2")

		src.set("4", articleDoc{ID: "4", Title: "没有同步过的文章"}) // 绕过 Sync 的修改，重建后补上
		src.set("5", articleDoc{ID: "5", Title: "无法建索引", Status: "bad"})
		runWorker(t, db, func(w *queue.Worker) { articlesV2.Handle(w, db, load, src.scan) })
		op := waitOperation(t, db, id)
		if op.Status != operation.StatusCompleted || op.Processed != 5 || op.Failed != 1 || op.Total != 5 {
			t.Fatalf("operation = %+v, want completed with 5 processed (3 has no documents), 1 failed", op)
		}
		eventually(t, "old version to be dropped", func() bool { return slices.Equal(versions(t, db), []int{2}) })
		if got := findV2(t, db, search.Query{Filter: search.Eq("lang", "zh")}); !slices.Equal(got, []string{"1", "2", "4"}) {
			t.Fatalf("lang = zh: %v, want [1 2 4]", got)
		}
		if got := findV2(t, db, search.Query{Text: "标题"}); !slices.Equal(got, []string{"2"}) {
			t.Fatalf("text: %v, want [2]", got)
		}

		// 切换之后同步只写入新版本
		src.set("1", articleDoc{ID: "1", Title: "再次修改"})
		if err := articlesV2.Refresh(ctx, db, load, "1"); err != nil {
			t.Fatal(err)
		}
		if got := findV2(t, db, search.Query{Text: "再次"}); !slices.Equal(got, []string{"1"}) || !slices.Equal(versions(t, db), []int{2}) {
			t.Fatalf("after switch: %v, versions %v", got, versions(t, db))
		}
	})
}

func TestReindexSuperseded(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, src := fixture(t, driver, articleDoc{ID: "1", Title: "一"}, articleDoc{ID: "2", Title: "二"})
		first := startReindex(t, db)
		second := startReindex(t, db)
		runWorker(t, db, func(w *queue.Worker) { articlesV2.Handle(w, db, loadV2(src), src.scan) })

		if op := waitOperation(t, db, first); op.Status != operation.StatusFailed || op.Error != "search.reindex_superseded" {
			t.Fatalf("first = %+v, want failed as superseded", op)
		}
		if op := waitOperation(t, db, second); op.Status != operation.StatusCompleted || op.Processed != 2 {
			t.Fatalf("second = %+v, want completed", op)
		}
		eventually(t, "only version 3 to remain", func() bool { return slices.Equal(versions(t, db), []int{3}) })
		if got := findV2(t, db, search.Query{Filter: search.Eq("lang", "zh")}); !slices.Equal(got, []string{"1", "2"}) {
			t.Fatalf("lang = zh: %v", got)
		}
	})
}

func TestReindexResumes(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, src := fixture(t, driver)
		for _, key := range []string{"1", "2", "3", "4", "5"} {
			src.set(key, articleDoc{ID: key, Title: "文章" + key})
		}
		var mu sync.Mutex
		var afters []string
		scan := func(ctx context.Context, db bun.IDB, after string, limit int) ([]string, error) {
			mu.Lock()
			afters = append(afters, after)
			n := len(afters)
			mu.Unlock()
			if n == 2 { // 第一批保存之后出错一次，任务立即重试
				return nil, queue.RetryAfter(errors.New("scan failed"), 0)
			}
			return src.scan(ctx, db, after, limit)
		}
		id := startReindex(t, db)
		runWorker(t, db, func(w *queue.Worker) { articlesV2.Handle(w, db, loadV2(src), scan) })

		if op := waitOperation(t, db, id); op.Status != operation.StatusCompleted || op.Processed != 5 {
			t.Fatalf("operation = %+v, want completed with 5 processed", op)
		}
		mu.Lock()
		defer mu.Unlock()
		if want := []string{"", "2", "2", "4", "5"}; !slices.Equal(afters, want) {
			t.Fatalf("scan calls after %q, want %q (resumed from the saved key)", afters, want)
		}
		if got := findV2(t, db, search.Query{Filter: search.Eq("lang", "zh")}); len(got) != 5 {
			t.Fatalf("lang = zh: %v", got)
		}
	})
}

func TestReindexWithConcurrentSyncs(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db, src := fixture(t, driver)
		load := loadV2(src)
		title := func(key string, n int) string { return "文章" + key + "版本" + strconv.Itoa(n) }
		var all []string
		for i := range 40 {
			key := fmt.Sprintf("%02d", i)
			all = append(all, key)
			src.set(key, articleDoc{ID: key, Title: title(key, 0)})
			if err := articles.Refresh(ctx, db, src.load, key); err != nil {
				t.Fatal(err)
			}
		}
		id := startReindex(t, db)
		runWorker(t, db, func(w *queue.Worker) { articlesV2.Handle(w, db, load, src.scan) })

		// 重建期间不停地修改和同步，最后索引中的每个文档都应当是数据的最新状态
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for g := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 1; n <= 15; n++ {
					key := all[(g*7+n*3)%len(all)]
					src.set(key, articleDoc{ID: key, Title: title(key, g*100+n)})
					if err := articlesV2.Refresh(ctx, db, load, key); err != nil {
						errs <- err
						return
					}
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if op := waitOperation(t, db, id); op.Status != operation.StatusCompleted {
			t.Fatalf("operation = %+v", op)
		}
		res, err := articlesV2.Search(ctx, db, search.Query{Page: page.OffsetParams{PerPage: 100}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Total != int64(len(all)) {
			t.Fatalf("total = %d, want %d", res.Total, len(all))
		}
		for _, h := range res.Items {
			docs, _ := src.load(ctx, db, h.Key)
			if h.Doc.Title != docs[0].Title || h.Doc.Lang != "zh" {
				t.Fatalf("%s: indexed %+v, want title %q", h.Key, h.Doc, docs[0].Title)
			}
		}
	})
}
