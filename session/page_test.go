package session_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx/web"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
)

func component(format string, args ...any) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	})
}

func pageServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := defaults()
	cfg.Cookie.Secure = false
	sm := session.New(cfg, memstore.New(), nil)
	pages := web.New(web.Options{
		ErrorPage:     func(status int, _ string) templ.Component { return component("page %d", status) },
		ErrorFragment: func(status int, _ string) templ.Component { return component("fragment %d", status) },
		ErrorTarget:   "#flash",
	})
	router := chi.NewRouter()
	router.Use(sm.LoadAndSave)
	router.Post("/login", func(w http.ResponseWriter, r *http.Request) {
		sub := identity.Subject{ID: r.FormValue("user"), Type: identity.SubjectUser}
		if sub.ID == "root" {
			sub.Scopes = []string{"admin"}
		}
		if err := session.Login(r.Context(), sm, sub); err != nil {
			t.Error(err)
		}
	})
	me := func(w http.ResponseWriter, r *http.Request) {
		sub, _ := identity.FromContext(r.Context())
		fmt.Fprintf(w, "%s via %s", sub.ID, sub.Method)
	}
	router.With(session.RequirePage(pages, sm, "/login")).Get("/me", me)
	router.With(session.RequirePage(pages, sm, "/login", "admin")).Get("/admin", me)
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

func TestRequirePage(t *testing.T) {
	srv := pageServer(t)
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	resp, _ := get(t, c, srv.URL+"/me?tab=1")
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/login?next=%2Fme%3Ftab%3D1" {
		t.Fatalf("anonymous page: %d %v", resp.StatusCode, resp.Header)
	}
	resp, _ = get(t, c, srv.URL+"/me", "HX-Request", "true", "HX-Current-URL", srv.URL+"/articles?page=2")
	if resp.StatusCode != 204 || resp.Header.Get("HX-Redirect") != "/login?next=%2Farticles%3Fpage%3D2" {
		t.Fatalf("anonymous htmx: %d %v", resp.StatusCode, resp.Header)
	}

	if resp, err := c.PostForm(srv.URL+"/login", map[string][]string{"user": {"alice"}}); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	if resp, body := get(t, c, srv.URL+"/me"); resp.StatusCode != 200 || body != "alice via session" {
		t.Fatalf("signed in: %d %q", resp.StatusCode, body)
	}
	if resp, body := get(t, c, srv.URL+"/admin"); resp.StatusCode != 403 || body != "page 403" {
		t.Fatalf("missing scope: %d %q", resp.StatusCode, body)
	}
	resp, body := get(t, c, srv.URL+"/admin", "HX-Request", "true", "HX-Request-Type", "partial")
	if resp.StatusCode != 403 || body != "fragment 403" || resp.Header.Get("HX-Retarget") != "#flash" {
		t.Fatalf("missing scope htmx: %d %q %v", resp.StatusCode, body, resp.Header)
	}
}
