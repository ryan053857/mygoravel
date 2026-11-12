package vector_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io/fs"
	"net"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/qdrant/go-client/qdrant"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/ai/aitest"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/queue/queuetest"
	"github.com/ryan053857/mygoravel/vector"
	"github.com/ryan053857/mygoravel/vector/vectortest"
)

const dims = 32

type chunkDoc struct {
	File        string     `json:"file" vector:"filter"`
	Tenant      string     `json:"tenant" vector:"tenant"`
	Text        string     `json:"text" vector:"embed"`
	Pages       []int      `json:"pages" vector:"filter"`
	Score       float64    `json:"score" vector:"filter"`
	Public      bool       `json:"public" vector:"filter"`
	PublishedAt *time.Time `json:"published_at" vector:"filter"`
}

var chunks = vector.Define[chunkDoc]("chunks")

// source 是测试中的数据源：load 返回 key 当前的文档。
type source struct {
	mu   sync.Mutex
	docs map[string][]chunkDoc
}

func (s *source) set(key string, docs ...chunkDoc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.docs[key] = docs
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

func (s *source) load(_ context.Context, _ bun.IDB, key string) ([]chunkDoc, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.docs[key], nil
}

// migrations 合并队列、操作和向量索引的迁移，按这个顺序排列版本号。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": queue.Migrations(), "2": operation.Migrations(), "3": vector.Migrations()} {
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

type fixture struct {
	db    *bun.DB
	emb   *aitest.Embedder
	store *vector.Store
	src   *source
}

func setup(t *testing.T) *fixture {
	t.Helper()
	db := dbtest.Open(t, migrations(t))
	emb := aitest.NewEmbedder(dims)
	return &fixture{
		db: db, emb: emb,
		store: vectortest.New(t, db, vector.Model{Name: "test", Dims: dims, Embedder: emb}),
		src:   &source{docs: map[string][]chunkDoc{}},
	}
}

// put 设置 key 的文档并立即同步。
func (f *fixture) put(t *testing.T, key string, docs ...chunkDoc) {
	t.Helper()
	f.src.set(key, docs...)
	if err := chunks.Refresh(context.Background(), f.store, f.src.load, key); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) search(t *testing.T, q vector.Query) []vector.Hit[chunkDoc] {
	t.Helper()
	hits, err := chunks.Search(context.Background(), f.store, q)
	if err != nil {
		t.Fatal(err)
	}
	return hits
}

func texts(hits []vector.Hit[chunkDoc]) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.Doc.Text
	}
	return out
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

func TestSearch(t *testing.T) {
	f := setup(t)
	if hits := f.search(t, vector.Query{Text: "还没有同步"}); len(hits) != 0 {
		t.Fatalf("hits before any sync = %v", hits)
	}
	f.put(t, "a", chunkDoc{File: "a", Text: "付款条件"}, chunkDoc{File: "a", Text: "交货时间"})
	f.put(t, "b", chunkDoc{File: "b", Text: "违约责任"})

	// aitest.Embedder 给相同的文本相同的向量，不同的文本几乎正交：查询原文时它排在第一，相似度为 1
	hits := f.search(t, vector.Query{Text: "交货时间"})
	if len(hits) != 3 || hits[0].Doc.Text != "交货时间" || hits[0].Key != "a" || hits[0].Score < 0.999 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[1].Score >= hits[0].Score {
		t.Fatalf("hits are not ordered by score: %+v", hits)
	}
	if hits := f.search(t, vector.Query{Text: "交货时间", Limit: 1}); len(hits) != 1 {
		t.Fatalf("limit 1 returned %d hits", len(hits))
	}
	if hits := f.search(t, vector.Query{Text: "交货时间", MinScore: 0.9}); !slices.Equal(texts(hits), []string{"交货时间"}) {
		t.Fatalf("min score hits = %v", texts(hits))
	}
	if _, err := chunks.Search(context.Background(), f.store, vector.Query{}); err == nil {
		t.Fatal("empty query text succeeded")
	}
}

func TestReplaceAndReuse(t *testing.T) {
	f := setup(t)
	f.put(t, "a", chunkDoc{Text: "一"}, chunkDoc{Text: "二"}, chunkDoc{Text: "三"})
	if got := f.emb.Requests(); len(got) != 1 || !slices.Equal(got[0], []string{"一", "二", "三"}) {
		t.Fatalf("embedding requests = %v", got)
	}

	// 文本不变的文档沿用原来的向量，即使位置变了；重复的文本只计算一次
	f.put(t, "a", chunkDoc{Text: "三"}, chunkDoc{Text: "四"}, chunkDoc{Text: "四"})
	if got := f.emb.Requests(); len(got) != 2 || !slices.Equal(got[1], []string{"四"}) {
		t.Fatalf("embedding requests = %v", got)
	}
	if got := sorted(texts(f.search(t, vector.Query{Text: "一"}))); !slices.Equal(got, []string{"三", "四", "四"}) {
		t.Fatalf("documents after replace = %v", got)
	}

	// 什么都没变时不调用模型（上面的查询计算了查询文本的向量）
	before := len(f.emb.Requests())
	f.put(t, "a", chunkDoc{Text: "三"}, chunkDoc{Text: "四"}, chunkDoc{Text: "四"})
	if n := len(f.emb.Requests()); n != before {
		t.Fatalf("embedding requests = %v, want no new request", f.emb.Requests())
	}

	f.put(t, "a")
	if hits := f.search(t, vector.Query{Text: "一"}); len(hits) != 0 {
		t.Fatalf("documents after delete = %v", texts(hits))
	}
	var n int
	if err := f.db.NewRaw("SELECT COUNT(*) FROM vector_keys").Scan(context.Background(), &n); err != nil || n != 0 {
		t.Fatalf("vector_keys rows = %d, %v", n, err)
	}
}

func TestFilters(t *testing.T) {
	f := setup(t)
	day := func(d int) *time.Time {
		v := time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC)
		return &v
	}
	f.put(t, "a",
		chunkDoc{File: "a", Text: "a1", Pages: []int{1, 2}, Score: 0.5, Public: true, PublishedAt: day(1)},
		chunkDoc{File: "a", Text: "a2", Pages: []int{3}, Score: 1.5, PublishedAt: day(2)},
	)
	f.put(t, "b", chunkDoc{File: "b", Text: "b1", Pages: []int{1}, Score: 2.5, Public: true})
	for _, c := range []struct {
		name   string
		filter vector.Filter
		want   []string
	}{
		{"eq keyword", vector.Eq("file", "a"), []string{"a1", "a2"}},
		{"eq multi", vector.Eq("pages", 1), []string{"a1", "b1"}},
		{"in", vector.In("pages", 2, 3), []string{"a1", "a2"}},
		{"in keyword", vector.In("file", "b", "c"), []string{"b1"}},
		{"in empty", vector.In[string]("file"), nil},
		{"range int", vector.Gte("pages", 2), []string{"a1", "a2"}},
		{"range float", vector.Gt("score", 1.5), []string{"b1"}},
		{"eq float", vector.Eq("score", 1.5), []string{"a2"}},
		{"in float", vector.In("score", 0.5, 2.5), []string{"a1", "b1"}},
		{"bool", vector.Eq("public", true), []string{"a1", "b1"}},
		{"time", vector.Lt("published_at", *day(2)), []string{"a1"}},
		{"time eq", vector.Eq("published_at", *day(2)), []string{"a2"}},
		{"and", vector.And(vector.Eq("file", "a"), vector.Eq("public", true)), []string{"a1"}},
		{"or", vector.Or(vector.Eq("file", "b"), vector.Eq("pages", 3)), []string{"a2", "b1"}},
		{"or empty", vector.Or(), nil},
		{"and empty", vector.And(), []string{"a1", "a2", "b1"}},
		{"not", vector.Not(vector.Eq("pages", 1)), []string{"a2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := sorted(texts(f.search(t, vector.Query{Text: "a1", Filter: c.filter})))
			if !slices.Equal(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
	for name, filter := range map[string]vector.Filter{
		"unknown field":   vector.Eq("text", "a1"),
		"keyword range":   vector.Gt("file", "a"),
		"wrong type":      vector.Eq("file", 1),
		"nil in and":      vector.And(nil),
		"bool range":      vector.Lt("public", true),
		"time wrong type": vector.Eq("published_at", "2026-11-01"),
	} {
		if _, err := chunks.Search(context.Background(), f.store, vector.Query{Text: "a1", Filter: filter}); err == nil {
			t.Errorf("%s: filter accepted", name)
		}
	}
}

func TestTenant(t *testing.T) {
	f := setup(t)
	f.put(t, "a", chunkDoc{Tenant: "t1", Text: "同一段话"})
	f.put(t, "b", chunkDoc{Tenant: "t2", Text: "同一段话"})
	f.put(t, "c", chunkDoc{Text: "同一段话"})
	for tenant, want := range map[string]string{"t1": "a", "t2": "b", "": "c"} {
		hits := f.search(t, vector.Query{Text: "同一段话", Tenant: tenant})
		if len(hits) != 1 || hits[0].Key != want {
			t.Fatalf("tenant %q hits = %+v", tenant, hits)
		}
	}

	plain := vector.Define[struct {
		Text string `json:"text" vector:"embed"`
	}]("plain")
	if _, err := plain.Search(context.Background(), f.store, vector.Query{Text: "x", Tenant: "t1"}); err == nil {
		t.Fatal("tenant on an index without a tenant field accepted")
	}
}

func TestModelMismatch(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	shared := newShared(t, f.db)
	old := shared.store(vector.Model{Name: "old", Dims: dims, Embedder: f.emb})
	f.src.set("a", chunkDoc{Text: "一"})
	if err := chunks.Refresh(ctx, old, f.src.load, "a"); err != nil {
		t.Fatal(err)
	}

	// 同一个 collection、没有配置建立它的模型的 Store：不能写入，也不能查询
	for _, m := range []vector.Model{{Name: "new", Dims: dims, Embedder: f.emb}, {Name: "old", Dims: dims * 2, Embedder: f.emb}} {
		changed := shared.store(m)
		if err := chunks.Refresh(ctx, changed, f.src.load, "a"); err == nil || !strings.Contains(err.Error(), "chunks_v1") {
			t.Fatalf("refresh with model %+v: %v", m, err)
		}
		if _, err := chunks.Search(ctx, changed, vector.Query{Text: "一"}); err == nil {
			t.Fatalf("search with model %+v succeeded", m)
		}
	}
}

// shared 创建使用同一个随机 collection 前缀的 Store，用于测试换模型。
type shared struct {
	db     *bun.DB
	client *qdrant.Client
	prefix string
}

func (sh *shared) store(m vector.Model, opts ...vector.Option) *vector.Store {
	return vector.New(sh.db, sh.client, m, append(opts, vector.WithPrefix(sh.prefix))...)
}

// exists 返回 collection（不含前缀）是否存在。
func (sh *shared) exists(t *testing.T, name string) bool {
	t.Helper()
	ok, err := sh.client.CollectionExists(context.Background(), sh.prefix+name)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// newShared 连接测试 Qdrant，测试结束时删除前缀下的 collection。
func newShared(t *testing.T, db *bun.DB) *shared {
	t.Helper()
	_, port, err := net.SplitHostPort(os.Getenv(vectortest.EnvAddr))
	if err != nil {
		t.Fatalf("%s: %v", vectortest.EnvAddr, err)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	client, err := qdrant.NewClient(&qdrant.Config{Host: "127.0.0.1", Port: n})
	if err != nil {
		t.Fatal(err)
	}
	prefix := "t" + strings.ToLower(rand.Text()[:10]) + "_"
	t.Cleanup(func() {
		defer func() { _ = client.Close() }()
		names, err := client.ListCollections(context.Background())
		if err != nil {
			t.Error(err)
		}
		for _, name := range names {
			if strings.HasPrefix(name, prefix) {
				_ = client.DeleteCollection(context.Background(), name)
			}
		}
	})
	return &shared{db: db, client: client, prefix: prefix}
}

func TestInvalidDocuments(t *testing.T) {
	f := setup(t)
	f.src.set("empty", chunkDoc{File: "x"})
	if err := chunks.Refresh(context.Background(), f.store, f.src.load, "empty"); err == nil {
		t.Fatal("document without text accepted")
	}
	if err := chunks.Refresh(context.Background(), f.store, f.src.load, ""); err == nil {
		t.Fatal("empty key accepted")
	}
	f.emb.FailWith(errors.New("model down"))
	f.src.set("a", chunkDoc{Text: "一"})
	if err := chunks.Refresh(context.Background(), f.store, f.src.load, "a"); err == nil {
		t.Fatal("embedding failure ignored")
	}
}

func TestSyncThroughQueue(t *testing.T) {
	ctx := context.Background()
	f := setup(t)
	f.src.set("1", chunkDoc{Text: "队列同步"})
	f.src.set("2", chunkDoc{Text: "回滚的修改"})

	err := database.RunInTx(ctx, f.db, nil, func(ctx context.Context, tx database.Tx) error {
		return chunks.Sync(ctx, tx, "1", "1")
	})
	if err != nil {
		t.Fatal(err)
	}
	rollback := database.RunInTx(ctx, f.db, nil, func(ctx context.Context, tx database.Tx) error {
		if err := chunks.Sync(ctx, tx, "2"); err != nil {
			return err
		}
		return context.Canceled
	})
	if rollback == nil {
		t.Fatal("rollback returned nil")
	}

	w := queue.NewWorker(f.db, queuetest.Config(t), nil)
	chunks.Handle(w, f.store, f.src.load, f.src.scan)
	queuetest.Run(t, w)
	deadline := time.Now().Add(10 * time.Second)
	for len(f.search(t, vector.Query{Text: "队列同步"})) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("document was not synced")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := texts(f.search(t, vector.Query{Text: "队列同步"})); !slices.Equal(got, []string{"队列同步"}) {
		t.Fatalf("documents = %v", got)
	}
}

func TestConcurrentRefresh(t *testing.T) {
	f := setup(t)
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 每次同步前把文档改成新的内容，最后一次同步读到的一定是最后写入的
			f.src.set("a", chunkDoc{Text: "版本"}, chunkDoc{Text: string(rune('A' + i))})
			errs <- chunks.Refresh(context.Background(), f.store, f.src.load, "a")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := chunks.Refresh(context.Background(), f.store, f.src.load, "a"); err != nil {
		t.Fatal(err)
	}
	want, _ := f.src.load(context.Background(), nil, "a")
	got := sorted(texts(f.search(t, vector.Query{Text: "版本"})))
	if !slices.Equal(got, sorted([]string{want[0].Text, want[1].Text})) {
		t.Fatalf("documents = %v, want %v", got, want)
	}
}

func TestDuplicateModels(t *testing.T) {
	f := setup(t)
	m := vector.Model{Name: "m", Dims: dims, Embedder: f.emb}
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate model accepted")
		}
	}()
	newShared(t, f.db).store(m, vector.WithPrevious(m))
}

func TestConcurrentFirstSync(t *testing.T) {
	// 索引还没有 collection 时多个进程（这里是共用前缀的 4 个 Store）的同步同时开始：
	// 一个创建，其余的等它就绪后照常写入
	for round := range 5 {
		f := setup(t)
		shared := newShared(t, f.db)
		stores := make([]*vector.Store, 4)
		for i := range stores {
			stores[i] = shared.store(vector.Model{Name: "test", Dims: dims, Embedder: f.emb})
		}
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		for i := range 32 {
			key := strconv.Itoa(i)
			f.src.set(key, chunkDoc{Text: "第" + key + "段"})
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs <- chunks.Refresh(context.Background(), stores[i%len(stores)], f.src.load, key)
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
	}
}
