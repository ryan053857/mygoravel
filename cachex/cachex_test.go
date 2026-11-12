package cachex_test

import (
	"context"
	"crypto/rand"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/bradfitz/gomemcache/memcache"

	"github.com/ryan053857/mygoravel/cachex"
)

type article struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

// fake 是记录写入的过期时间、可以让读写出错的 Memcached。
type fake struct {
	mu      sync.Mutex
	values  map[string]string
	ttls    map[string]int32
	getErr  error
	setErr  error
	deleted []string
}

func newFake() *fake { return &fake{values: map[string]string{}, ttls: map[string]int32{}} }

func (f *fake) Get(key string) (*memcache.Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
	}
	v, ok := f.values[key]
	if !ok {
		return nil, memcache.ErrCacheMiss
	}
	return &memcache.Item{Key: key, Value: []byte(v)}, nil
}

func (f *fake) Set(item *memcache.Item) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.setErr != nil {
		return f.setErr
	}
	f.values[item.Key] = string(item.Value)
	f.ttls[item.Key] = item.Expiration
	return nil
}

func (f *fake) Delete(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.values[key]; !ok {
		return memcache.ErrCacheMiss
	}
	delete(f.values, key)
	f.deleted = append(f.deleted, key)
	return nil
}

// ttl 返回 key 写入时的过期时间。
func (f *fake) ttl(key string) time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return time.Duration(f.ttls[key]) * time.Second
}

// openMemcached 连接 MEMCACHED_TEST_ADDR 指向的 Memcached。
func openMemcached(t *testing.T) *memcache.Client {
	t.Helper()
	addr := os.Getenv("MEMCACHED_TEST_ADDR")
	if addr == "" {
		t.Fatal("MEMCACHED_TEST_ADDR is required for this test")
	}
	mc := memcache.New(addr)
	t.Cleanup(func() { _ = mc.Close() })
	return mc
}

// counter 返回计数的加载函数。
func counter(n *atomic.Int64, v *article, err error) func(context.Context) (*article, error) {
	return func(context.Context) (*article, error) {
		n.Add(1)
		return v, err
	}
}

func TestRemember(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](openMemcached(t), "test:"+rand.Text()+":article:", 10*time.Minute)
	var loads atomic.Int64
	load := counter(&loads, &article{ID: 1, Title: "hello"}, nil)
	for range 3 {
		got, err := c.Remember(ctx, "1", load)
		if err != nil || got.Title != "hello" {
			t.Fatalf("got %+v err %v", got, err)
		}
	}
	if loads.Load() != 1 {
		t.Fatalf("loaded %d times", loads.Load())
	}
	if err := c.Forget(ctx, "1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(ctx, "1"); err != nil {
		t.Fatalf("forget a missing key: %v", err)
	}
	if _, err := c.Remember(ctx, "1", load); err != nil || loads.Load() != 2 {
		t.Fatalf("after forget: loaded %d times, err %v", loads.Load(), err)
	}
}

// 超过 250 字节或含有空白的 key 以摘要保存，仍然按 key 区分。
func TestIllegalKeys(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](openMemcached(t), "test:"+rand.Text()+":", time.Minute)
	var loads atomic.Int64
	for i, key := range []string{strings.Repeat("k", 300), strings.Repeat("k", 299) + "x", "with space", "with\nnewline"} {
		for range 2 {
			if got, err := c.Remember(ctx, key, counter(&loads, &article{ID: i}, nil)); err != nil || got.ID != i {
				t.Fatalf("%q: got %+v err %v", key[:10], got, err)
			}
		}
	}
	if loads.Load() != 4 {
		t.Fatalf("loaded %d times", loads.Load())
	}
}

// 超过 30 天的过期时间写成 Unix 时间。
func TestLongTTL(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	c := cachex.New[*article](f, "", 60*24*time.Hour)
	var loads atomic.Int64
	if _, err := c.Remember(ctx, "1", counter(&loads, &article{ID: 1}, nil)); err != nil {
		t.Fatal(err)
	}
	at := time.Unix(int64(f.ttls["1"]), 0)
	if until := time.Until(at); until < 59*24*time.Hour || until > 67*24*time.Hour {
		t.Fatalf("expires at %v", at)
	}
}

// 热点键过期时，并发的读取只加载一次，每个调用者得到独立的一份。
func TestConcurrentLoadsMerge(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](newFake(), "", time.Minute)
	var loads atomic.Int64
	release := make(chan struct{})
	load := func(context.Context) (*article, error) {
		loads.Add(1)
		<-release
		return &article{ID: 1, Title: "hello"}, nil
	}
	const n = 50
	results := make([]*article, n)
	var wg sync.WaitGroup
	var started sync.WaitGroup
	started.Add(n)
	for i := range n {
		wg.Go(func() {
			started.Done()
			got, err := c.Remember(ctx, "article:1", load)
			if err != nil {
				t.Error(err)
			}
			results[i] = got
		})
	}
	started.Wait()
	time.Sleep(20 * time.Millisecond) // 让调用者都进入等待
	close(release)
	wg.Wait()
	if loads.Load() != 1 {
		t.Fatalf("loaded %d times", loads.Load())
	}
	results[0].Title = "changed"
	for _, r := range results[1:] {
		if r.Title != "hello" {
			t.Fatal("callers share the same value")
		}
	}
}

// 一个调用者取消只影响它自己，加载继续，其他调用者得到结果。
func TestCallerCancellation(t *testing.T) {
	c := cachex.New[*article](newFake(), "", time.Minute)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var loadErr atomic.Value
	load := func(ctx context.Context) (*article, error) {
		// 第二个调用者可能在加载结束之后才到达，再加载一次，所以只关闭一次。
		once.Do(func() { close(started) })
		<-release
		loadErr.Store(ctx.Err() == nil)
		return &article{ID: 1}, nil
	}
	first, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Remember(first, "article:1", load)
		done <- err
	}()
	<-started
	second := make(chan *article, 1)
	go func() {
		got, _ := c.Remember(context.Background(), "article:1", load)
		second <- got
	}()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first caller: %v", err)
	}
	close(release)
	if got := <-second; got == nil || got.ID != 1 {
		t.Fatalf("second caller got %+v", got)
	}
	if loadErr.Load() != true {
		t.Fatal("load context was canceled")
	}
}

func TestLoadErrorsAndPanics(t *testing.T) {
	ctx := context.Background()
	c := cachex.New[*article](newFake(), "", time.Minute)
	var loads atomic.Int64
	boom := errors.New("database down")
	for range 2 {
		if _, err := c.Remember(ctx, "article:1", counter(&loads, nil, boom)); !errors.Is(err, boom) {
			t.Fatalf("err %v", err)
		}
	}
	if loads.Load() != 2 {
		t.Fatalf("errors must not be cached: loaded %d times", loads.Load())
	}
	_, err := c.Remember(ctx, "article:2", func(context.Context) (*article, error) { panic("oops") })
	if err == nil || !strings.HasPrefix(err.Error(), "cachex: panic: oops") {
		t.Fatalf("panic: %v", err)
	}
}

// 过期时间加上至多 10% 的抖动。
func TestTTL(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	c := cachex.New[*article](f, "", 10*time.Minute)
	var loads atomic.Int64
	seen := map[time.Duration]bool{}
	for i := range 20 {
		key := "article:" + string(rune('a'+i))
		if _, err := c.Remember(ctx, key, counter(&loads, &article{ID: i}, nil)); err != nil {
			t.Fatal(err)
		}
		ttl := f.ttl(key)
		if ttl < 10*time.Minute || ttl > 11*time.Minute { // 抖动至多 10%，再向上取整到秒
			t.Fatalf("ttl %v", ttl)
		}
		seen[ttl] = true
	}
	if len(seen) < 2 {
		t.Fatal("ttl has no jitter")
	}
}

// codeError 像 errorx 的错误一样按错误码匹配。
type codeError struct{ code string }

func (e *codeError) Error() string { return e.code }

func (e *codeError) Is(target error) bool {
	t, ok := target.(*codeError)
	return ok && t.code == e.code
}

func errNotFound() error { return &codeError{"article.not_found"} }

// Missing 声明的错误缓存约一分钟，期间返回新的同类错误；其他错误和 nil 值照常处理。
func TestMissing(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	c := cachex.New[*article](f, "article:", 10*time.Minute, cachex.Missing(errNotFound))
	var loads atomic.Int64
	notFound := func(context.Context) (*article, error) {
		loads.Add(1)
		return nil, errors.Join(errors.New("select"), errNotFound()) // 包装过的错误同样匹配
	}
	for range 3 {
		if _, err := c.Remember(ctx, "404", notFound); !errors.Is(err, errNotFound()) {
			t.Fatalf("err %v", err)
		}
	}
	if ttl := f.ttl("article:404"); loads.Load() != 1 || ttl < time.Minute || ttl > time.Minute+6*time.Second {
		t.Fatalf("missing: loaded %d times, ttl %v", loads.Load(), ttl)
	}

	// 其他错误不缓存。
	boom := errors.New("database down")
	for range 2 {
		if _, err := c.Remember(ctx, "500", counter(&loads, nil, boom)); !errors.Is(err, boom) {
			t.Fatalf("err %v", err)
		}
	}
	if loads.Load() != 3 {
		t.Fatalf("loaded %d times", loads.Load())
	}

	// nil 是普通的值，按 ttl 缓存。
	for range 2 {
		if got, err := c.Remember(ctx, "nil", counter(&loads, nil, nil)); err != nil || got != nil {
			t.Fatalf("got %+v err %v", got, err)
		}
	}
	if ttl := f.ttl("article:nil"); loads.Load() != 4 || ttl < 10*time.Minute {
		t.Fatalf("nil: loaded %d times, ttl %v", loads.Load(), ttl)
	}

	// 没有声明 Missing 的 Cache 把标记当作无法解码的内容，重新加载。
	plain := cachex.New[*article](f, "article:", 10*time.Minute)
	if got, err := plain.Remember(ctx, "404", counter(&loads, &article{ID: 404}, nil)); err != nil || got.ID != 404 {
		t.Fatalf("plain got %+v err %v", got, err)
	}
}

// 缓存出错或内容无法解码时直接加载，结果照常返回。
func TestDegradation(t *testing.T) {
	ctx := context.Background()
	f := newFake()
	c := cachex.New[*article](f, "", time.Minute)
	var loads atomic.Int64
	load := counter(&loads, &article{ID: 1, Title: "hello"}, nil)

	f.values["article:1"] = "{not json"
	if got, err := c.Remember(ctx, "article:1", load); err != nil || got.Title != "hello" || f.values["article:1"] == "{not json" {
		t.Fatalf("corrupt: got %+v err %v value %q", got, err, f.values["article:1"])
	}

	f.getErr, f.setErr = errors.New("connection refused"), errors.New("connection refused")
	for range 2 {
		if got, err := c.Remember(ctx, "article:1", load); err != nil || got.Title != "hello" {
			t.Fatalf("cache down: got %+v err %v", got, err)
		}
	}
	if loads.Load() != 3 {
		t.Fatalf("loaded %d times", loads.Load())
	}
}

func TestInvalidArguments(t *testing.T) {
	for name, f := range map[string]func(){
		"nil client":  func() { cachex.New[int](nil, "", time.Minute) },
		"ttl":         func() { cachex.New[int](newFake(), "", 0) },
		"nil missing": func() { cachex.Missing(nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: expected a panic", name)
				}
			}()
			f()
		}()
	}
}

// 缓存后端不可用时，每个 Cache 每分钟只记录一条日志，附上省略的条数。
func TestLogThrottle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var mu sync.Mutex
		var records []slog.Record
		old := slog.Default()
		slog.SetDefault(slog.New(handlerFunc(func(r slog.Record) {
			mu.Lock()
			records = append(records, r)
			mu.Unlock()
		})))
		defer slog.SetDefault(old)

		ctx := context.Background()
		f := newFake()
		f.getErr, f.setErr = errors.New("connection refused"), errors.New("connection refused")
		c := cachex.New[*article](f, "", time.Minute)
		load := func(context.Context) (*article, error) { return &article{ID: 1}, nil }
		for range 5 {
			if _, err := c.Remember(ctx, "1", load); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(time.Minute)
		if _, err := c.Remember(ctx, "1", load); err != nil {
			t.Fatal(err)
		}
		mu.Lock()
		defer mu.Unlock()
		if len(records) != 2 {
			t.Fatalf("%d log records", len(records))
		}
		var suppressed int64
		records[1].Attrs(func(a slog.Attr) bool {
			if a.Key == "suppressed" {
				suppressed = a.Value.Int64()
			}
			return true
		})
		// 前 5 次各有读、写两条，记录了第一条；之后的读被记录，它前面省略了 9 条。
		if suppressed != 9 {
			t.Fatalf("suppressed %d", suppressed)
		}
	})
}

type handlerFunc func(slog.Record)

func (h handlerFunc) Enabled(context.Context, slog.Level) bool { return true }
func (h handlerFunc) Handle(_ context.Context, r slog.Record) error {
	h(r)
	return nil
}
func (h handlerFunc) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h handlerFunc) WithGroup(string) slog.Handler      { return h }
