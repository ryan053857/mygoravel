package webhook_test

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/outbound"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/webhook"
)

var (
	acme = identity.Subject{ID: "u1", Type: "user", Tenant: "acme"}
)

// migrations 合并队列和 webhook 的迁移，队列的版本号在前。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": queue.Migrations(), "2": webhook.Migrations()} {
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

func openDB(t *testing.T) *bun.DB {
	t.Helper()
	return dbtest.Open(t, migrations(t))
}

func create(t *testing.T, db bun.IDB, tenant, url string, events ...string) *webhook.Endpoint {
	t.Helper()
	ep, err := webhook.CreateEndpoint(context.Background(), db, tenant, webhook.EndpointInput{URL: url, Events: events})
	if err != nil {
		t.Fatal(err)
	}
	return ep
}

func deliveries(t *testing.T, db bun.IDB, q webhook.DeliveryQuery) []webhook.Delivery {
	t.Helper()
	list, err := webhook.ListDeliveries(context.Background(), db, q)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func jobCount(t *testing.T, db bun.IDB) int {
	t.Helper()
	n, err := db.NewSelect().Table("queue_jobs").Count(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return int(n)
}

func TestEndpoints(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()

	for name, in := range map[string]webhook.EndpointInput{
		"relative url":  {URL: "/hooks", Events: []string{"*"}},
		"ftp url":       {URL: "ftp://example.com", Events: []string{"*"}},
		"no events":     {URL: "https://example.com"},
		"bad wildcard":  {URL: "https://example.com", Events: []string{"article*"}},
		"empty prefix":  {URL: "https://example.com", Events: []string{".*"}},
		"empty event":   {URL: "https://example.com", Events: []string{""}},
		"inner pattern": {URL: "https://example.com", Events: []string{"a.*.b"}},
	} {
		if _, err := webhook.CreateEndpoint(ctx, db, "acme", in); err == nil {
			t.Errorf("%s: created", name)
		}
	}

	ep := create(t, db, "acme", "https://acme.example/hooks", "article.*")
	if !strings.HasPrefix(ep.Secret, "whsec_") || ep.Disabled {
		t.Fatalf("endpoint %+v", ep)
	}
	got, err := webhook.GetEndpoint(ctx, db, "acme", ep.ID)
	if err != nil || got.URL != ep.URL || got.Secret != ep.Secret || len(got.Events) != 1 || got.Events[0] != "article.*" {
		t.Fatalf("get %+v, %v", got, err)
	}
	// 其他租户看不到、改不了、删不掉。
	if _, err := webhook.GetEndpoint(ctx, db, "globex", ep.ID); !errors.Is(err, webhook.ErrEndpointNotFound) {
		t.Fatalf("other tenant get: %v", err)
	}
	if _, err := webhook.UpdateEndpoint(ctx, db, "globex", ep.ID, webhook.EndpointInput{URL: "https://x.example", Events: []string{"*"}}); !errors.Is(err, webhook.ErrEndpointNotFound) {
		t.Fatalf("other tenant update: %v", err)
	}
	if err := webhook.DeleteEndpoint(ctx, db, "globex", ep.ID); !errors.Is(err, webhook.ErrEndpointNotFound) {
		t.Fatalf("other tenant delete: %v", err)
	}

	updated, err := webhook.UpdateEndpoint(ctx, db, "acme", ep.ID, webhook.EndpointInput{
		URL: "https://acme.example/v2", Events: []string{"*"}, Description: "all", Disabled: true,
	})
	if err != nil || updated.URL != "https://acme.example/v2" || !updated.Disabled || updated.Description != "all" || updated.Secret != ep.Secret {
		t.Fatalf("update %+v, %v", updated, err)
	}
	// 重复提交相同内容也算成功。
	if _, err := webhook.UpdateEndpoint(ctx, db, "acme", ep.ID, webhook.EndpointInput{
		URL: "https://acme.example/v2", Events: []string{"*"}, Description: "all", Disabled: true,
	}); err != nil {
		t.Fatalf("same update: %v", err)
	}

	rotated, err := webhook.RotateSecret(ctx, db, "acme", ep.ID)
	if err != nil || rotated.Secret == ep.Secret || rotated.PreviousSecret != ep.Secret ||
		rotated.PreviousSecretExpiresAt.Before(time.Now().Add(23*time.Hour)) {
		t.Fatalf("rotate %+v, %v", rotated, err)
	}

	create(t, db, "acme", "https://acme.example/second", "*")
	create(t, db, "globex", "https://globex.example", "*")
	list, err := webhook.ListEndpoints(ctx, db, "acme")
	if err != nil || len(list) != 2 || list[0].ID != ep.ID {
		t.Fatalf("list %d, %v", len(list), err)
	}

	if err := webhook.DeleteEndpoint(ctx, db, "acme", ep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := webhook.GetEndpoint(ctx, db, "acme", ep.ID); !errors.Is(err, webhook.ErrEndpointNotFound) {
		t.Fatalf("deleted endpoint: %v", err)
	}
}

func TestSend(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	send := func(sub identity.Subject, typ string) {
		t.Helper()
		if err := (webhook.Event[any]{Name: typ}).Send(ctx, db, sub, map[string]any{"id": 1}); err != nil {
			t.Fatal(err)
		}
	}

	// 没有端点时什么也不写。
	send(acme, "article.published")
	if n := jobCount(t, db); n != 0 {
		t.Fatalf("jobs without endpoints: %d", n)
	}

	exact := create(t, db, "acme", "https://a.example/1", "article.published")
	prefix := create(t, db, "acme", "https://a.example/2", "article.*")
	all := create(t, db, "acme", "https://a.example/3", "*")
	create(t, db, "acme", "https://a.example/4", "comment.*")
	disabled := create(t, db, "acme", "https://a.example/5", "*")
	if _, err := webhook.UpdateEndpoint(ctx, db, "acme", disabled.ID, webhook.EndpointInput{URL: disabled.URL, Events: disabled.Events, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	create(t, db, "globex", "https://g.example", "*")

	send(acme, "article.published")
	list := deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme"})
	got := map[uuid.UUID]bool{}
	for _, d := range list {
		got[d.EndpointID] = true
		if d.EventID != list[0].EventID || d.Status != webhook.StatusPending || d.Event != "article.published" || d.Attempts != 0 {
			t.Fatalf("delivery %+v", d)
		}
	}
	if len(list) != 3 || !got[exact.ID] || !got[prefix.ID] || !got[all.ID] {
		t.Fatalf("delivered to %v", got)
	}
	var body struct {
		Type      string         `json:"type"`
		Timestamp time.Time      `json:"timestamp"`
		Data      map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(list[0].Payload), &body); err != nil || body.Type != "article.published" || body.Data["id"] != float64(1) || body.Timestamp.IsZero() {
		t.Fatalf("payload %s: %v", list[0].Payload, err)
	}
	if n := jobCount(t, db); n != 3 {
		t.Fatalf("jobs %d", n)
	}
	if n := len(deliveries(t, db, webhook.DeliveryQuery{Tenant: "globex"})); n != 0 {
		t.Fatalf("globex deliveries %d", n)
	}

	// 只匹配前缀的事件，以及事务回滚。
	send(acme, "article.deleted")
	if n := len(deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", Status: webhook.StatusPending})); n != 5 {
		t.Fatalf("after prefix event: %d", n)
	}
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		if err := (webhook.Event[any]{Name: "article.published"}).Send(ctx, tx, acme, nil); err != nil {
			return err
		}
		return errors.New("rollback")
	})
	if err == nil || len(deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme"})) != 5 || jobCount(t, db) != 5 {
		t.Fatalf("rollback kept deliveries: %v", err)
	}

	for _, typ := range []string{"", "article.*"} {
		if err := (webhook.Event[any]{Name: typ}).Send(ctx, db, acme, nil); err == nil {
			t.Errorf("type %q accepted", typ)
		}
	}

	// 查询条件与翻页。
	first := deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", EndpointID: all.ID})
	if len(first) != 2 || first[0].Event != "article.deleted" {
		t.Fatalf("by endpoint %+v", first)
	}
	page := deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", EndpointID: all.ID, Before: first[0].ID, Limit: 1})
	if len(page) != 1 || page[0].ID != first[1].ID {
		t.Fatalf("page %+v", page)
	}
	if n := len(deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", EventID: first[0].EventID})); n != 2 {
		t.Fatalf("by event %d", n)
	}
	if _, err := webhook.GetDelivery(ctx, db, "globex", first[0].ID); !errors.Is(err, webhook.ErrDeliveryNotFound) {
		t.Fatalf("other tenant delivery: %v", err)
	}

	// 删除端点同时删除它的投递记录。
	if err := webhook.DeleteEndpoint(ctx, db, "acme", all.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", EndpointID: all.ID})); n != 0 {
		t.Fatalf("deliveries of deleted endpoint %d", n)
	}
}

// receiver 是测试用的接收方，按顺序返回 codes 中的状态码，之后返回 204。
type receiver struct {
	mu       sync.Mutex
	codes    []int
	requests []*http.Request
	bodies   []string
	srv      *httptest.Server
}

func newReceiver(t *testing.T, codes ...int) *receiver {
	r := &receiver{codes: codes}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		r.requests = append(r.requests, req)
		r.bodies = append(r.bodies, string(body))
		code := http.StatusNoContent
		if len(r.codes) > 0 {
			code, r.codes = r.codes[0], r.codes[1:]
		}
		if code == http.StatusFound {
			w.Header().Set("Location", "/elsewhere")
		}
		w.WriteHeader(code)
		_, _ = io.WriteString(w, "receiver says no")
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// get 返回第 i 个请求和它的请求体。
func (r *receiver) get(i int) (*http.Request, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.requests[i], r.bodies[i]
}

func (r *receiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// local 是放行本机测试接收方的投递客户端。
func local() *http.Client {
	return outbound.New(outbound.Options{AnyPort: true, Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}})
}

// startWorker 运行处理投递任务的 worker，可以投递到本机的测试接收方，第一次失败后约 1 秒重试。
func startWorker(t *testing.T, db *bun.DB, maxAttempts int) {
	t.Helper()
	runWorker(t, db, webhook.Options{Client: local(), Timeout: 2 * time.Second, MaxAttempts: maxAttempts})
}

// runWorker 用 opts 运行处理投递任务的 worker。
func runWorker(t *testing.T, db *bun.DB, opts webhook.Options) {
	t.Helper()
	cfg := queue.DefaultConfig()
	cfg.PollInterval = 20 * time.Millisecond
	w := queue.NewWorker(queue.Database(db), cfg, slog.New(slog.DiscardHandler))
	webhook.NewDeliverer(db, opts).Handle(w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func waitFor(t *testing.T, db bun.IDB, tenant string, id uuid.UUID, status webhook.Status) *webhook.Delivery {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		d, err := webhook.GetDelivery(context.Background(), db, tenant, id)
		if err != nil {
			t.Fatal(err)
		}
		if d.Status == status {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("delivery %+v did not become %s", d, status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestDeliver(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	ok := newReceiver(t)
	flaky := newReceiver(t, http.StatusInternalServerError)
	broken := newReceiver(t, http.StatusFound, http.StatusFound)
	okEP := create(t, db, "acme", ok.srv.URL, "*")
	flakyEP := create(t, db, "acme", flaky.srv.URL, "*")
	brokenEP := create(t, db, "acme", broken.srv.URL, "*")
	startWorker(t, db, 2)

	if err := (webhook.Event[map[string]string]{Name: "article.published"}).Send(ctx, db, acme, map[string]string{"title": "hi"}); err != nil {
		t.Fatal(err)
	}
	byEndpoint := map[uuid.UUID]webhook.Delivery{}
	for _, d := range deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme"}) {
		byEndpoint[d.EndpointID] = d
	}

	d := waitFor(t, db, "acme", byEndpoint[okEP.ID].ID, webhook.StatusSucceeded)
	if d.Attempts != 1 || d.ResponseStatus != http.StatusNoContent || d.LastError != "" {
		t.Fatalf("succeeded %+v", d)
	}
	req, body := ok.get(0)
	if req.Header.Get(webhook.HeaderID) != d.EventID.String() || req.Header.Get("Content-Type") != "application/json" || body != d.Payload {
		t.Fatalf("request headers %v body %s", req.Header, body)
	}
	if err := webhook.Verify(okEP.Secret, req.Header, []byte(body)); err != nil {
		t.Fatalf("verify: %v", err)
	}

	// 第一次 500，重试后成功。
	d = waitFor(t, db, "acme", byEndpoint[flakyEP.ID].ID, webhook.StatusSucceeded)
	first, firstBody := flaky.get(0)
	second, secondBody := flaky.get(1)
	if d.Attempts != 2 || flaky.count() != 2 || firstBody != secondBody ||
		first.Header.Get(webhook.HeaderID) != second.Header.Get(webhook.HeaderID) {
		t.Fatalf("flaky %+v, %d requests", d, flaky.count())
	}

	// 重定向视为失败，次数用完后标记为失败，不进入失败任务表。
	d = waitFor(t, db, "acme", byEndpoint[brokenEP.ID].ID, webhook.StatusFailed)
	if d.Attempts != 2 || d.ResponseStatus != http.StatusFound || !strings.Contains(d.LastError, "HTTP 302: receiver says no") {
		t.Fatalf("failed %+v", d)
	}
	if n, err := db.NewSelect().Table("queue_failed_jobs").Count(ctx); err != nil || n != 0 {
		t.Fatalf("failed jobs %d, %v", n, err)
	}

	// 重发写入一条新记录，事件 ID 和请求体不变。
	again, err := webhook.Resend(ctx, db, "acme", d.ID)
	if err != nil || again.ID == d.ID || again.EventID != d.EventID || again.Payload != d.Payload {
		t.Fatalf("resend %+v, %v", again, err)
	}
	if got := waitFor(t, db, "acme", again.ID, webhook.StatusSucceeded); got.Attempts != 1 {
		t.Fatalf("resent %+v", got)
	}
	if _, err := webhook.Resend(ctx, db, "globex", d.ID); !errors.Is(err, webhook.ErrDeliveryNotFound) {
		t.Fatalf("other tenant resend: %v", err)
	}
}

// 默认客户端拒绝内网地址：投递直接标记为失败，不再重试，接收方收不到请求。
func TestDeliverRefusesPrivateAddresses(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	create(t, db, "acme", rcv.srv.URL, "*")
	runWorker(t, db, webhook.Options{MaxAttempts: 5})
	if err := (webhook.Event[map[string]string]{Name: "article.published"}).Send(ctx, db, acme, map[string]string{"title": "hi"}); err != nil {
		t.Fatal(err)
	}
	list := deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme"})
	d := waitFor(t, db, "acme", list[0].ID, webhook.StatusFailed)
	if d.Attempts != 1 || d.ResponseStatus != 0 || !strings.Contains(d.LastError, "destination not allowed") || rcv.count() != 0 {
		t.Fatalf("delivery %+v, %d requests", d, rcv.count())
	}
}

func TestDeliverRotatedAndDisabled(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	rcv := newReceiver(t)
	ep := create(t, db, "acme", rcv.srv.URL, "*")
	rotated, err := webhook.RotateSecret(ctx, db, "acme", ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	off := create(t, db, "acme", rcv.srv.URL+"/off", "*")
	if err := (webhook.Event[any]{Name: "x"}).Send(ctx, db, acme, nil); err != nil {
		t.Fatal(err)
	}
	// 发布之后才停用：投递时标记为失败，不发送。
	if _, err := webhook.UpdateEndpoint(ctx, db, "acme", off.ID, webhook.EndpointInput{URL: off.URL, Events: off.Events, Disabled: true}); err != nil {
		t.Fatal(err)
	}
	startWorker(t, db, 2)

	list := deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", EndpointID: off.ID})
	d := waitFor(t, db, "acme", list[0].ID, webhook.StatusFailed)
	if d.Attempts != 0 || d.LastError != "endpoint disabled" {
		t.Fatalf("disabled %+v", d)
	}
	list = deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme", EndpointID: ep.ID})
	waitFor(t, db, "acme", list[0].ID, webhook.StatusSucceeded)
	if rcv.count() != 1 {
		t.Fatalf("requests %d", rcv.count())
	}
	// 轮换期间新旧密钥都能校验。
	req, body := rcv.get(0)
	if sigs := strings.Fields(req.Header.Get(webhook.HeaderSignature)); len(sigs) != 2 {
		t.Fatalf("signatures %v", sigs)
	}
	for _, secret := range []string{rotated.Secret, ep.Secret} {
		if err := webhook.Verify(secret, req.Header, []byte(body)); err != nil {
			t.Fatalf("verify with %s: %v", secret, err)
		}
	}
}

func TestCleanup(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	create(t, db, "acme", "https://a.example", "*")
	for range 3 {
		if err := (webhook.Event[any]{Name: "x"}).Send(ctx, db, acme, nil); err != nil {
			t.Fatal(err)
		}
	}
	old := deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme"})[1:]
	ids := []uuid.UUID{old[0].ID, old[1].ID}
	if _, err := db.NewUpdate().Model((*webhook.Delivery)(nil)).Set("created_at = ?", time.Now().Add(-48*time.Hour).UTC()).
		Where("id IN (?)", bun.List(ids)).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	cleanup := webhook.Cleanup(db, 24*time.Hour)
	if n, err := cleanup(ctx, 1); err != nil || n != 1 {
		t.Fatalf("first batch %d, %v", n, err)
	}
	if n, err := cleanup(ctx, 10); err != nil || n != 1 {
		t.Fatalf("second batch %d, %v", n, err)
	}
	if n := len(deliveries(t, db, webhook.DeliveryQuery{Tenant: "acme"})); n != 1 {
		t.Fatalf("left %d", n)
	}
	if _, err := cleanup(ctx, 0); err == nil {
		t.Fatal("limit 0 accepted")
	}
}
