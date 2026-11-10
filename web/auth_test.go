package web_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

func authServer(t *testing.T, store scs.Store) *httptest.Server {
	t.Helper()
	o := options()
	o.Sessions.Store = store
	pages := web.New(o)
	router := chi.NewRouter()
	public := pages.Routes(router, web.Public)
	public.Post("/login", func(w http.ResponseWriter, r *http.Request) error {
		sub := identity.Subject{ID: r.FormValue("user"), Type: identity.SubjectUser}
		if sub.ID == "root" {
			sub.Scopes = []string{"admin"}
		}
		if err := pages.Login(r.Context(), sub); err != nil {
			return err
		}
		web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/me"))
		return nil
	})
	me := func(w http.ResponseWriter, r *http.Request) error {
		sub, _ := identity.FromContext(r.Context())
		_, err := fmt.Fprintf(w, "%s via %s", sub.ID, sub.Method)
		return err
	}
	pages.Routes(router, web.Require()).Get("/me", me)
	pages.Routes(router, web.Require("admin")).Get("/admin", me)
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

func TestRejectsCrossOrigin(t *testing.T) {
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
	h := handle(web.New(o), func(http.ResponseWriter, *http.Request) error { return nil })
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
