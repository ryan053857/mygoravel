package web_test

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

// 页面路由的 context 带有重新校验会话的函数：会话被登出或撤销、用户被停用、失去 scope 后，
// 长连接据此结束；Resolve 暂时故障时连接保持。
func TestRequireInstallsCredentialCheck(t *testing.T) {
	var (
		mu     sync.Mutex
		scopes = []string{"events"}
		state  = "active"
	)
	set := func(f func()) { mu.Lock(); f(); mu.Unlock() }
	store := memstore.New()
	o := options()
	o.Sessions.Store = store
	o.Resolve = func(_ context.Context, stored identity.Subject) (identity.Subject, error) {
		mu.Lock()
		defer mu.Unlock()
		switch state {
		case "down":
			return identity.Subject{}, errorx.Define(errorx.Unavailable, "users.unavailable").New()
		case "disabled":
			return identity.Subject{}, errorx.Define(errorx.Unauthenticated, "users.disabled").New()
		}
		return identity.Subject{ID: stored.ID, Type: stored.Type, Permissions: scopes}, nil
	}
	pages := web.New(o)
	router := chi.NewRouter()
	pages.Routes(router, web.Public).Post("/login", func(w http.ResponseWriter, r *http.Request) error {
		return pages.Login(r.Context(), identity.Subject{ID: "alice", Type: identity.SubjectUser})
	})
	captured := make(chan context.Context, 1)
	pages.Routes(router, web.Require("events")).Get("/events", func(w http.ResponseWriter, r *http.Request) error {
		captured <- r.Context()
		return nil
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp, err := c.Post(srv.URL+"/login", "", nil); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if resp, err := c.Get(srv.URL + "/events"); err != nil || resp.StatusCode != 200 {
		t.Fatalf("events: %v %v", resp, err)
	}
	ctx := <-captured
	token := jar.Cookies(mustURL(t, srv.URL))[0].Value

	if err := httpx.CheckCredential(ctx); err != nil {
		t.Fatalf("live session: %v", err)
	}
	set(func() { state = "down" })
	if err := httpx.CheckCredential(ctx); err != nil {
		t.Fatalf("resolver unavailable treated as revoked: %v", err)
	}
	set(func() { state = "active"; scopes = nil })
	if err := httpx.CheckCredential(ctx); httpx.StatusOf(err) != http.StatusForbidden {
		t.Fatalf("scope removed: %v", err)
	}
	set(func() { scopes = []string{"events"}; state = "disabled" })
	if err := httpx.CheckCredential(ctx); httpx.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("user disabled: %v", err)
	}
	set(func() { state = "active" })
	if err := httpx.CheckCredential(ctx); err != nil {
		t.Fatalf("re-enabled: %v", err)
	}
	// 会话在别处被登出或撤销（这里直接从存储删除）。
	if err := store.Delete(token); err != nil {
		t.Fatal(err)
	}
	if err := httpx.CheckCredential(ctx); httpx.StatusOf(err) != http.StatusUnauthorized {
		t.Fatalf("session revoked: %v", err)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
