package clientgen_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/article"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/identity"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/page"
	"github.com/go-tenon/tenon/httpx/rest/clientgen/internal/example/api/wire"
)

// serve 用示例接口启动服务端，返回它的地址。
func serve(t *testing.T, valid tokens) string {
	t.Helper()
	router := chi.NewRouter()
	router.Use(httpx.Locale(nil))
	router.Use(httpx.AppVersion(httpx.AppVersionConfig{Minimum: map[string]string{"ios": "2.0.0"}}))
	outer := chi.NewRouter()
	outer.Mount("/api", router)
	_ = exampleAPIOn(router, valid)
	srv := httptest.NewServer(outer)
	t.Cleanup(srv.Close)
	return srv.URL
}

func exampleAPIOn(router chi.Router, valid tokens) *rest.API {
	api := exampleAPI(rest.ModeServe, valid)
	router.Mount("/", api.Handler())
	return api
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	base := serve(t, tokens{"secret": true})
	anonymous := api.New(api.Options{BaseURL: base, Language: func() string { return "en" }})
	c := api.New(api.Options{BaseURL: base, Auth: wire.StaticToken("secret")})

	pong, err := anonymous.Ping(ctx)
	if err != nil || pong != (article.PingOutputBody{OK: true}) {
		t.Fatal(pong, err)
	}

	changes := article.Changes{Title: "Hello", Slug: "hello", Tags: []string{"go", "news"}}
	_, err = anonymous.Article.Create(ctx, changes)
	var apiErr *wire.Error
	if !errors.Is(err, identity.ErrUnauthenticated) || !errors.As(err, &apiErr) ||
		apiErr.Status != 401 || apiErr.Detail != "Authentication required" || apiErr.RequestID != "" {
		t.Fatalf("create without token: %#v", err)
	}

	created, err := c.Article.Create(ctx, changes)
	if err != nil {
		t.Fatal(err)
	}
	a := created.Body
	if created.Location != "/articles/"+a.ID.String() || a.Status != article.StatusDraft || a.Author.Name != "alice" || a.PublishedAt != nil {
		t.Fatalf("created %+v", created)
	}

	if _, err := c.Article.Create(ctx, changes); !errors.Is(err, article.ErrSlugTaken) || errors.Is(err, article.ErrNotFound) {
		t.Fatalf("duplicate slug: %v", err)
	}
	_, err = c.Article.Create(ctx, article.Changes{Slug: "Bad Slug"})
	if !errors.As(err, &apiErr) || apiErr.Status != 422 || apiErr.Code != "" || len(apiErr.Fields) != 2 {
		t.Fatalf("invalid input: %#v", err)
	}

	got, err := anonymous.Article.Get(ctx, a.ID)
	if err != nil || got.Slug != "hello" {
		t.Fatal(got, err)
	}
	if _, err := anonymous.Article.Get(ctx, uuid.New()); !errors.Is(err, article.ErrNotFound) {
		t.Fatalf("missing article: %v", err)
	}

	changes.Title = "Hello again"
	if updated, err := c.Article.Update(ctx, a.ID, changes); err != nil || updated.Title != "Hello again" {
		t.Fatal(updated, err)
	}

	if featured, err := c.Article.Feature(ctx, a.ID, article.FeatureInputBody{Featured: true}); err != nil || !featured.Featured {
		t.Fatal(featured, err)
	}

	key := article.PublishInput{IdempotencyKey: "k1"}
	published, err := c.Article.Publish(ctx, a.ID, key)
	if err != nil || published.Status != article.StatusPublished || published.PublishedAt == nil || published.PublishedAt.Year() != 2026 {
		t.Fatal(published, err)
	}
	if _, err := c.Article.Publish(ctx, a.ID, key); err != nil {
		t.Fatal("the idempotency key header must be sent:", err)
	}
	if _, err := c.Article.Publish(ctx, a.ID, article.PublishInput{}); !errors.As(err, &apiErr) || apiErr.Code != "article.internal" || apiErr.Status != 500 {
		t.Fatalf("publishing twice: %#v", err)
	}

	for _, slug := range []string{"second", "third"} {
		if _, err := c.Article.Create(ctx, article.Changes{Title: "Other " + slug, Slug: slug, Tags: []string{"go"}}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := anonymous.Article.List(ctx, article.ListInput{Tags: []string{"go"}, Q: "Other", OffsetParams: page.OffsetParams{Page: 2, PerPage: 1}})
	if err != nil || list.Total != 2 || list.Page != 2 || len(list.Items) != 1 || list.Items[0].Slug != "third" {
		t.Fatal(list, err)
	}
	list, err = anonymous.Article.List(ctx, article.ListInput{Status: article.StatusPublished, Tags: []string{"go", "news"}})
	if err != nil || list.Total != 1 || list.PerPage != 20 || list.Items[0].ID != a.ID {
		t.Fatal(list, err)
	}

	if err := c.Article.Delete(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.Article.Delete(ctx, a.ID); !errors.Is(err, article.ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}

	old := api.New(api.Options{BaseURL: base, AppVersion: "ios/1.9.0"})
	if _, err := old.Ping(ctx); !errors.Is(err, wire.ErrUpgradeRequired) {
		t.Fatalf("outdated app: %v", err)
	}
}

// rotating 是会过期的 token：Refresh 把 current 换成 next。
type rotating struct {
	mu        sync.Mutex
	current   string
	next      string
	refreshes atomic.Int32
	fail      bool
}

func (r *rotating) Token(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current, nil
}

func (r *rotating) Refresh(context.Context) error {
	r.refreshes.Add(1)
	if r.fail {
		return errors.New("refresh token revoked")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = r.next
	return nil
}

func TestConcurrentUnauthorizedRequestsRefreshOnce(t *testing.T) {
	ctx := context.Background()
	base := serve(t, tokens{"fresh": true})
	auth := &rotating{current: "stale", next: "fresh"}
	c := api.New(api.Options{BaseURL: base, Auth: auth})

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			_, err := c.Article.Create(ctx, article.Changes{Title: "t", Slug: "slug-" + string(rune('a'+i))})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := auth.refreshes.Load(); n != 1 {
		t.Fatalf("refreshed %d times", n)
	}

	revoked := &rotating{current: "stale", fail: true}
	_, err := api.New(api.Options{BaseURL: base, Auth: revoked}).Article.Create(ctx, article.Changes{Title: "t", Slug: "x"})
	if !errors.Is(err, identity.ErrUnauthenticated) || revoked.refreshes.Load() != 1 {
		t.Fatalf("failed refresh must return the original 401: %v", err)
	}
}

func TestPublicEndpointsNeverSendTheToken(t *testing.T) {
	var seen atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Store(r.Header.Get("Authorization") + "|" + r.URL.Path + "|" + r.Header.Get("X-App-Version"))
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	c := api.New(api.Options{BaseURL: srv.URL + "/", Auth: wire.StaticToken("secret"), AppVersion: "macos/1.0.0"})
	if _, err := c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load(); got != "|/api/ping|macos/1.0.0" {
		t.Fatalf("request %q", got)
	}
}
