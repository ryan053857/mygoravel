package search_test

import (
	"context"
	"io/fs"
	"log/slog"
	"path"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/page"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/search"
)

type articleDoc struct {
	ID          string     `json:"id"`
	Tenant      string     `json:"tenant" search:"tenant"`
	Title       string     `json:"title" search:"text,boost"`
	Body        string     `json:"body" search:"text"`
	Tags        []string   `json:"tags" search:"filter"`
	Status      string     `json:"status" search:"filter"`
	Views       int        `json:"views" search:"filter"`
	PublishedAt *time.Time `json:"published_at" search:"filter"`
}

var articles = search.Define[articleDoc]("articles")

// source 是测试中的数据源：Load 返回 key 当前的文档。
type source struct {
	mu   sync.Mutex
	docs map[string][]articleDoc
}

func newSource() *source { return &source{docs: map[string][]articleDoc{}} }

func (s *source) set(key string, docs ...articleDoc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[key] = docs
}

func (s *source) load(_ context.Context, _ bun.IDB, key string) ([]articleDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.docs[key], nil
}

// scan 按 key 的顺序返回 after 之后的 key，每次最多 2 个，让重建分成多批。
func (s *source) scan(_ context.Context, _ bun.IDB, after string, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for key := range s.docs {
		if key > after {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out[:min(len(out), limit, 2)], nil
}

// migrations 合并队列、操作和搜索的迁移，按这个顺序排列版本号。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": queue.Migrations(), "2": operation.Migrations(), "3": search.Migrations()} {
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

// fixture 打开数据库，写入 docs（每个文档的 key 是它的 ID）并同步。
func fixture(t *testing.T, driver database.Driver, docs ...articleDoc) (*bun.DB, *source) {
	t.Helper()
	db := dbtesting.Open(t, driver, migrations(t))
	src := newSource()
	for _, d := range docs {
		src.set(d.ID, d)
		if err := articles.Refresh(context.Background(), db, src.load, d.ID); err != nil {
			t.Fatal(err)
		}
	}
	return db, src
}

func find(t *testing.T, db bun.IDB, q search.Query) *search.Result[articleDoc] {
	t.Helper()
	res, err := articles.Search(context.Background(), db, q)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func keys(res *search.Result[articleDoc]) []string {
	out := make([]string, len(res.Items))
	for i, h := range res.Items {
		out[i] = h.Key
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func expectKeys(t *testing.T, res *search.Result[articleDoc], want ...string) {
	t.Helper()
	if got := keys(res); !slices.Equal(sorted(got), sorted(want)) {
		t.Fatalf("keys = %v, want %v", got, want)
	}
	if res.Total != len(want) {
		t.Fatalf("total = %d, want %d", res.Total, len(want))
	}
}

func TestText(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, _ := fixture(t, driver,
			articleDoc{ID: "1", Title: "全文搜索的设计方案", Body: "用 PostgreSQL 实现"},
			articleDoc{ID: "2", Title: "数据库设计", Body: "Database indexes and query plans"},
			articleDoc{ID: "3", Title: "Go 并发", Body: "goroutine 和 channel，ＡＢＣ全角"},
		)
		for _, c := range []struct {
			text string
			want []string
		}{
			{"搜索", []string{"1"}},
			{"设计", []string{"1", "2"}},
			{"设计方案", []string{"1"}},
			{"搜", []string{"1"}},        // 一个字按前缀命中“搜索”
			{"database", []string{"2"}}, // 不区分大小写
			{"datab", []string{"2"}},    // 最后一个词按前缀匹配
			{"datab plans", nil},        // 只有最后一个词按前缀匹配
			{"DataBase 设计", []string{"2"}},
			{"postgresql", []string{"1"}},
			{"abc", []string{"3"}}, // 全角转为半角
			{"并发 goroutine", []string{"3"}},
			{"搜索 goroutine", nil},
			{"不存在", nil},
			{"", []string{"1", "2", "3"}},   // 没有查询文本时列出所有文档
			{"！？", []string{"1", "2", "3"}}, // 没有切分出词，同上
		} {
			t.Run(c.text, func(t *testing.T) {
				expectKeys(t, find(t, db, search.Query{Text: c.text}), c.want...)
			})
		}
	})
}

func TestBoostAndHighlights(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		long := strings.Repeat("前面的内容很长。", 30) + "这里提到了搜索引擎。" + strings.Repeat("后面的内容。", 30)
		db, _ := fixture(t, driver,
			articleDoc{ID: "body", Title: "一篇文章", Body: long},
			articleDoc{ID: "title", Title: "搜索引擎入门", Body: "介绍倒排表"},
		)
		res := find(t, db, search.Query{Text: "搜索引擎"})
		if got := keys(res); !slices.Equal(got, []string{"title", "body"}) {
			t.Fatalf("order = %v, want the title match first", got)
		}
		if res.Items[0].Score <= res.Items[1].Score {
			t.Fatalf("scores = %v, %v", res.Items[0].Score, res.Items[1].Score)
		}

		title := res.Items[0].Highlights["title"]
		if want := (search.Snippet{{Text: "搜索引擎", Mark: true}, {Text: "入门"}}); !slices.Equal(title, want) {
			t.Fatalf("title highlight = %#v", title)
		}
		if _, ok := res.Items[0].Highlights["body"]; ok {
			t.Fatal("body without a match is highlighted")
		}
		body := res.Items[1].Highlights["body"]
		s := body.String()
		if !strings.HasPrefix(s, "…") || !strings.HasSuffix(s, "…") || !strings.Contains(s, "提到了搜索引擎") {
			t.Fatalf("body snippet = %q", s)
		}
		if n := len([]rune(s)); n > search.SnippetLength+2 {
			t.Fatalf("snippet has %d runes", n)
		}
		var marked []string
		for _, p := range body {
			if p.Mark {
				marked = append(marked, p.Text)
			}
		}
		if !slices.Equal(marked, []string{"搜索引擎"}) {
			t.Fatalf("marked = %q", marked)
		}
	})
}

func TestFiltersSortAndFacets(t *testing.T) {
	day := func(d int) *time.Time {
		t := time.Date(2026, 11, d, 8, 0, 0, 0, time.UTC)
		return &t
	}
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, _ := fixture(t, driver,
			articleDoc{ID: "a", Title: "Go 入门", Tags: []string{"go", "入门"}, Status: "published", Views: 120, PublishedAt: day(1)},
			articleDoc{ID: "b", Title: "Go 进阶", Tags: []string{"go"}, Status: "published", Views: -5, PublishedAt: day(3)},
			articleDoc{ID: "c", Title: "Rust 入门", Tags: []string{"rust", "入门"}, Status: "published", Views: 30, PublishedAt: day(2)},
			articleDoc{ID: "d", Title: "Go 草稿", Tags: []string{"go"}, Status: "draft", Views: 0},
		)
		for _, c := range []struct {
			name   string
			filter search.Filter
			want   []string
		}{
			{"eq", search.Eq("status", "draft"), []string{"d"}},
			{"multi-valued", search.Eq("tags", "入门"), []string{"a", "c"}},
			{"in", search.In("tags", "rust", "missing"), []string{"c"}},
			{"empty in", search.In[string]("tags"), nil},
			{"negative ints", search.Lt("views", 0), []string{"b"}},
			{"int range", search.And(search.Gte("views", int64(30)), search.Lte("views", 120)), []string{"a", "c"}},
			{"time", search.Gt("published_at", *day(1)), []string{"b", "c"}},
			{"or", search.Or(search.Eq("status", "draft"), search.Eq("tags", "rust")), []string{"c", "d"}},
			{"not", search.Not(search.Eq("tags", "go")), []string{"c"}},
			{"empty and", search.And(), []string{"a", "b", "c", "d"}},
			{"empty or", search.Or(), nil},
		} {
			t.Run(c.name, func(t *testing.T) {
				expectKeys(t, find(t, db, search.Query{Filter: c.filter}), c.want...)
			})
		}
		t.Run("with text", func(t *testing.T) {
			expectKeys(t, find(t, db, search.Query{Text: "go", Filter: search.Eq("status", "published")}), "a", "b")
		})

		t.Run("sort", func(t *testing.T) {
			if got := keys(find(t, db, search.Query{Sort: []search.Sort{search.Desc("views")}})); !slices.Equal(got, []string{"a", "c", "d", "b"}) {
				t.Fatalf("views desc = %v", got)
			}
			// 没有值的排在最后，无论升序还是降序
			if got := keys(find(t, db, search.Query{Sort: []search.Sort{search.Asc("published_at")}})); !slices.Equal(got, []string{"a", "c", "b", "d"}) {
				t.Fatalf("published_at asc = %v", got)
			}
			if got := keys(find(t, db, search.Query{Sort: []search.Sort{search.Desc("published_at")}})); !slices.Equal(got, []string{"b", "c", "a", "d"}) {
				t.Fatalf("published_at desc = %v", got)
			}
		})

		t.Run("facets", func(t *testing.T) {
			res := find(t, db, search.Query{Filter: search.Eq("status", "published"), Facets: []string{"tags", "views"}})
			want := []search.FacetValue{{Value: "go", Count: 2}, {Value: "入门", Count: 2}, {Value: "rust", Count: 1}}
			if got := res.Facets["tags"]; !slices.Equal(got, want) {
				t.Fatalf("tags facet = %v", got)
			}
			if got := res.Facets["views"]; !slices.Equal(got, []search.FacetValue{{"-5", 1}, {"30", 1}, {"120", 1}}) {
				t.Fatalf("views facet = %v", got)
			}
			res = find(t, db, search.Query{Text: "入门", Facets: []string{"tags"}, FacetLimit: 1})
			if got := res.Facets["tags"]; !slices.Equal(got, []search.FacetValue{{"入门", 2}}) {
				t.Fatalf("limited facet = %v", got)
			}
		})

		t.Run("paging", func(t *testing.T) {
			res := find(t, db, search.Query{Page: page.OffsetParams{Page: 2, PerPage: 3}})
			if got := keys(res); !slices.Equal(got, []string{"d"}) || res.Total != 4 || res.LastPage() != 2 {
				t.Fatalf("page 2 = %v, total %d", got, res.Total)
			}
		})

		t.Run("errors", func(t *testing.T) {
			for name, q := range map[string]search.Query{
				"unknown filter": {Filter: search.Eq("title", "x")},
				"wrong type":     {Filter: search.Eq("views", "many")},
				"sort by slice":  {Sort: []search.Sort{search.Asc("tags")}},
				"unknown facet":  {Facets: []string{"body"}},
				"nil filter":     {Filter: search.And(nil)},
			} {
				if _, err := articles.Search(context.Background(), db, q); err == nil {
					t.Errorf("%s: no error", name)
				}
			}
		})
	})
}

func TestTenants(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, _ := fixture(t, driver,
			articleDoc{ID: "1", Tenant: "acme", Title: "季度报告"},
			articleDoc{ID: "2", Tenant: "globex", Title: "季度报告"},
			articleDoc{ID: "3", Title: "平台公告"},
		)
		expectKeys(t, find(t, db, search.Query{Text: "报告", Tenant: "acme"}), "1")
		expectKeys(t, find(t, db, search.Query{Tenant: "globex"}), "2")
		expectKeys(t, find(t, db, search.Query{}), "3")

		type note struct {
			Text string `json:"text" search:"text"`
		}
		notes := search.Define[note]("notes")
		if _, err := notes.Search(context.Background(), db, search.Query{Tenant: "acme"}); err == nil {
			t.Fatal("tenant query on an index without a tenant field succeeded")
		}
	})
}

func TestUpdateDeleteAndChunks(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db, src := fixture(t, driver, articleDoc{ID: "1", Title: "旧标题", Tags: []string{"old"}})
		other := search.Define[articleDoc]("other")
		if err := other.Refresh(ctx, db, src.load, "1"); err != nil {
			t.Fatal(err)
		}

		src.set("1", articleDoc{ID: "1", Title: "新标题", Tags: []string{"new"}})
		if err := articles.Refresh(ctx, db, src.load, "1"); err != nil {
			t.Fatal(err)
		}
		expectKeys(t, find(t, db, search.Query{Text: "旧标题"}))
		expectKeys(t, find(t, db, search.Query{Text: "新标题"}), "1")
		expectKeys(t, find(t, db, search.Query{Filter: search.Eq("tags", "old")}))

		// 一个 key 多个文档，例如附件的分段
		src.set("file", articleDoc{ID: "file", Body: "第一段讲安装"}, articleDoc{ID: "file", Body: "第二段讲配置"}, articleDoc{ID: "file", Body: "第三段讲安装后的检查"})
		if err := articles.Refresh(ctx, db, src.load, "file"); err != nil {
			t.Fatal(err)
		}
		res := find(t, db, search.Query{Text: "安装"})
		if got := keys(res); !slices.Equal(got, []string{"file", "file"}) {
			t.Fatalf("chunks = %v", got)
		}

		src.set("1")
		src.set("file", articleDoc{ID: "file", Body: "只剩一段"})
		if err := articles.Refresh(ctx, db, src.load, "1", "file"); err != nil {
			t.Fatal(err)
		}
		expectKeys(t, find(t, db, search.Query{}), "file")
		expectKeys(t, find(t, db, search.Query{Text: "安装"}))

		// 其他索引中同一个 key 不受影响
		res2, err := other.Search(ctx, db, search.Query{Text: "旧标题"})
		if err != nil || res2.Total != 1 {
			t.Fatalf("other index = %+v, %v", res2, err)
		}
		// 旧的过滤值都已删除：other 中的 1 有 tags、status、views，file 有 status、views
		var n int
		if err := db.NewRaw("SELECT COUNT(*) FROM search_values").Scan(ctx, &n); err != nil || n != 5 {
			t.Fatalf("search_values rows = %d, %v", n, err)
		}
	})
}

func TestInvalidDocuments(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, src := fixture(t, driver)
		src.set("1", articleDoc{ID: "1", Title: "x", Status: strings.Repeat("s", 192)})
		if err := articles.Refresh(context.Background(), db, src.load, "1"); err == nil {
			t.Fatal("refresh of a too long value succeeded")
		}
		if err := articles.Refresh(context.Background(), db, src.load, strings.Repeat("k", 192)); err == nil {
			t.Fatal("refresh of a too long key succeeded")
		}
		expectKeys(t, find(t, db, search.Query{}))
	})
}

// runWorker 在后台运行一个处理 db 中任务的 worker，测试结束时停止它。register 注册处理函数。
func runWorker(t *testing.T, db *bun.DB, register func(w *queue.Worker)) {
	t.Helper()
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	w := queue.NewWorker(queue.Database(db), cfg, slog.New(slog.DiscardHandler))
	register(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestSyncThroughQueue(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		ctx := context.Background()
		db, src := fixture(t, driver)
		src.set("1", articleDoc{ID: "1", Title: "队列同步"})
		src.set("2", articleDoc{ID: "2", Title: "回滚的修改"})

		err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
			return articles.Sync(ctx, tx, "1", "1", "1")
		})
		if err != nil {
			t.Fatal(err)
		}
		var jobs int
		if err := db.NewRaw("SELECT COUNT(*) FROM queue_jobs").Scan(ctx, &jobs); err != nil || jobs != 1 {
			t.Fatalf("queued jobs = %d, %v; want the duplicates merged", jobs, err)
		}
		rollback := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
			if err := articles.Sync(ctx, tx, "2"); err != nil {
				return err
			}
			return context.Canceled
		})
		if rollback == nil {
			t.Fatal("rollback returned nil")
		}

		runWorker(t, db, func(w *queue.Worker) { articles.Handle(w, db, src.load, src.scan) })
		deadline := time.Now().Add(10 * time.Second)
		for find(t, db, search.Query{}).Total == 0 {
			if time.Now().After(deadline) {
				t.Fatal("document was not synced")
			}
			time.Sleep(20 * time.Millisecond)
		}
		expectKeys(t, find(t, db, search.Query{}), "1")
	})
}

func TestConcurrentRefresh(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db, src := fixture(t, driver)
		src.set("1", articleDoc{ID: "1", Title: "并发", Tags: []string{"a", "b"}}, articleDoc{ID: "1", Title: "同步"})
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for i := range 16 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				key := "1"
				if i%2 == 1 {
					key = "2"
					src.set("2", articleDoc{ID: "2", Title: "另一个"})
				}
				errs <- articles.Refresh(context.Background(), db, src.load, key)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		if got := find(t, db, search.Query{}).Total; got != 3 {
			t.Fatalf("total = %d, want 3", got)
		}
	})
}

func TestDefinePanics(t *testing.T) {
	for name, define := range map[string]func(){
		"bad name": func() { search.Define[articleDoc]("Bad Name") },
		"no text": func() {
			search.Define[struct {
				A string `search:"filter"`
			}]("x")
		},
		"unknown tag": func() {
			search.Define[struct {
				A string `search:"text,weight=3"`
			}]("x")
		},
		"bad filter type": func() {
			search.Define[struct {
				A string         `search:"text"`
				B map[string]int `search:"filter"`
			}]("x")
		},
		"json dash": func() {
			search.Define[struct {
				A string `json:"-" search:"text"`
			}]("x")
		},
		"duplicate name": func() {
			search.Define[struct {
				A string `json:"B" search:"text"`
				B string `search:"filter"`
			}]("x")
		},
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("no panic")
				}
			}()
			define()
		})
	}
}
