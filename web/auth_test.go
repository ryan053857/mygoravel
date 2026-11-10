package web_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
)

func authServer(t *testing.T, store scs.Store, configure ...func(*web.Options)) *httptest.Server {
	t.Helper()
	o := options()
	o.Sessions.Store = store
	for _, f := range configure {
		f(&o)
	}
	sm := o.Sessions
	pages := web.New(o)
	router := chi.NewRouter()
	router.Group(func(r chi.Router) {
		r.Use(pages.Middleware)
		r.Post("/login", pages.Handle(func(w http.ResponseWriter, r *http.Request) error {
			sub := identity.Subject{ID: r.FormValue("user"), Type: identity.SubjectUser}
			if sub.ID == "root" {
				sub.Scopes = []string{"admin"}
			}
			if err := session.Login(r.Context(), sm, sub); err != nil {
				return err
			}
			web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/me"))
			return nil
		}))
		me := func(w http.ResponseWriter, r *http.Request) {
			sub, _ := identity.FromContext(r.Context())
			fmt.Fprintf(w, "%s via %s", sub.ID, sub.Method)
		}
		r.With(pages.Require()).Get("/me", me)
		r.With(pages.Require("admin")).Get("/admin", me)
	})
	srv := httptest.NewServer(router)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, c *http.Client, url string, header ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for i := 0; i < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp, string(body)
}

func noFollow(t *testing.T) *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func TestRequire(t *testing.T) {
	srv := authServer(t, memstore.New())
	c := noFollow(t)

	resp, _ := get(t, c, srv.URL+"/me?tab=1")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login?next=%2Fme%3Ftab%3D1" {
		t.Fatalf("anonymous page: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, c, srv.URL+"/me", "HX-Request", "true", "HX-Current-URL", srv.URL+"/articles?page=2")
	if resp.StatusCode != 204 || resp.Header.Get("HX-Redirect") != "/login?next=%2Farticles%3Fpage%3D2" {
		t.Fatalf("anonymous htmx: %d %v", resp.StatusCode, resp.Header)
	}

	resp, err := c.PostForm(srv.URL+"/login?next=%2Fme", map[string][]string{"user": {"alice"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/me" {
		t.Fatalf("login: %d %v", resp.StatusCode, resp.Header)
	}
	if resp, body := get(t, c, srv.URL+"/me"); resp.StatusCode != 200 || body != "alice via session" {
		t.Fatalf("signed in: %d %q", resp.StatusCode, body)
	}
	if resp, body := get(t, c, srv.URL+"/admin"); resp.StatusCode != 403 || body != "page 403 Forbidden" {
		t.Fatalf("missing scope: %d %q", resp.StatusCode, body)
	}
	resp, body := get(t, c, srv.URL+"/admin", "HX-Request", "true", "HX-Request-Type", "partial")
	if resp.StatusCode != 403 || body != "fragment 403 Forbidden" || resp.Header.Get("HX-Retarget") != "#flash" {
		t.Fatalf("missing scope htmx: %d %q %v", resp.StatusCode, body, resp.Header)
	}
}

func TestRequireResolve(t *testing.T) {
	var (
		mu       sync.Mutex
		scopes   []string
		disabled bool
		down     bool
	)
	set := func(f func()) { mu.Lock(); f(); mu.Unlock() }
	// resolve 模拟每次请求查询用户的当前权限和状态，返回的主体不带 Method。
	resolve := func(_ *http.Request, stored identity.Subject) (identity.Subject, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case down:
			return identity.Subject{}, errorx.Define(errorx.Unavailable, "users.unavailable").New()
		case disabled:
			return identity.Subject{}, errorx.Define(errorx.Unauthenticated, "users.disabled").New()
		}
		return identity.Subject{ID: stored.ID, Type: stored.Type, Scopes: scopes}, nil
	}
	srv := authServer(t, memstore.New(), func(o *web.Options) { o.Resolve = resolve })
	c := noFollow(t)
	login := func() {
		t.Helper()
		resp, err := c.PostForm(srv.URL+"/login", map[string][]string{"user": {"alice"}})
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	login()
	if resp, _ := get(t, c, srv.URL+"/admin"); resp.StatusCode != 403 {
		t.Fatalf("before grant: %d", resp.StatusCode)
	}
	// 授权后不需要重新登录。
	set(func() { scopes = []string{"admin"} })
	if resp, body := get(t, c, srv.URL+"/admin"); resp.StatusCode != 200 || body != "alice via session" {
		t.Fatalf("after grant: %d %q", resp.StatusCode, body)
	}

	set(func() { down = true })
	if resp, body := get(t, c, srv.URL+"/me"); resp.StatusCode != 503 || !strings.HasPrefix(body, "page 503") {
		t.Fatalf("resolver down: %d %q", resp.StatusCode, body)
	}

	// 停用后跳转登录页，并销毁会话：恢复后旧会话也不再有效。
	set(func() { down, disabled = false, true })
	resp, _ := get(t, c, srv.URL+"/me")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login?next=%2Fme" {
		t.Fatalf("disabled: %d %v", resp.StatusCode, resp.Header)
	}
	set(func() { disabled = false })
	if resp, _ := get(t, c, srv.URL+"/me"); resp.StatusCode != 303 {
		t.Fatalf("session not destroyed: %d", resp.StatusCode)
	}
	login()
	if resp, _ := get(t, c, srv.URL+"/me"); resp.StatusCode != 200 {
		t.Fatalf("login again: %d", resp.StatusCode)
	}
}

func TestMiddlewareRejectsCrossOrigin(t *testing.T) {
	srv := authServer(t, memstore.New())
	req, _ := http.NewRequest("POST", srv.URL+"/login", strings.NewReader("user=alice"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := noFollow(t).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 403 || string(body) != "page 403 Forbidden" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}

type failingStore struct{ scs.Store }

func (failingStore) Find(string) ([]byte, bool, error) { return nil, false, context.DeadlineExceeded }

func TestSessionStoreFailureRendersErrorPage(t *testing.T) {
	srv := authServer(t, failingStore{memstore.New()})
	req, _ := http.NewRequest("GET", srv.URL+"/me", nil)
	req.AddCookie(&http.Cookie{Name: "session", Value: "token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 500 || string(body) != "page 500 Internal Server Error" {
		t.Fatalf("%d %q", resp.StatusCode, body)
	}
}

func TestTrustedOrigins(t *testing.T) {
	o := options()
	o.TrustedOrigins = []string{"https://admin.example.com"}
	pages := web.New(o)
	h := pages.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	req := httptest.NewRequest("POST", "/x", nil)
	req.Header.Set("Origin", "https://admin.example.com")
	if rr := serve(h, req); rr.Code != 200 {
		t.Fatalf("trusted origin: %d", rr.Code)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for malformed origin")
		}
	}()
	o.TrustedOrigins = []string{"not a url"}
	web.New(o)
}
