package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"testing/fstest"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/web"
)

const immutable = "public, max-age=31536000, immutable"

func staticFS() fstest.MapFS {
	return fstest.MapFS{
		"css/app.css":                   {Data: []byte("body{color:red}")},
		"js/bundle-86c5f2802d0bc25d.js": {Data: []byte("console.log(1)")},
		"img/logo.svg":                  {Data: []byte("<svg/>")},
		"fonts/inter.woff2":             {Data: []byte("woff2")},
		"js/vendor/lib.min.js":          {Data: []byte("lib")},
		"css/theme.css":                 {Data: []byte("body{color:red}")}, // 与 app.css 内容相同
	}
}

func newStatic(t *testing.T) *web.Static {
	t.Helper()
	s, err := web.NewStatic(staticFS(), web.StaticOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStaticURLFingerprints(t *testing.T) {
	s := newStatic(t)
	css := s.URL("css/app.css")
	if !regexp.MustCompile(`^/static/css/app\.[0-9a-f]{8}\.css$`).MatchString(css) {
		t.Errorf("css/app.css → %s", css)
	}
	if s.URL("css/theme.css") != "/static/css/theme"+css[len("/static/css/app"):] {
		t.Errorf("same content should get the same fingerprint: %s %s", css, s.URL("css/theme.css"))
	}
	for name, want := range map[string]string{
		"js/bundle-86c5f2802d0bc25d.js": "/static/js/bundle-86c5f2802d0bc25d.js", // 已带哈希，不再加指纹
		"missing.css":                   "/static/missing.css",
	} {
		if got := s.URL(name); got != want {
			t.Errorf("URL(%q) = %s, want %s", name, got, want)
		}
	}
	other, _ := web.NewStatic(fstest.MapFS{"css/app.css": {Data: []byte("body{color:blue}")}}, web.StaticOptions{})
	if other.URL("css/app.css") == css {
		t.Error("fingerprint did not change with content")
	}
}

func TestStaticServe(t *testing.T) {
	s := newStatic(t)
	cases := []struct {
		path, cache, body string
		status            int
	}{
		{s.URL("css/app.css"), immutable, "body{color:red}", 200},
		{"/static/css/app.css", "no-cache", "body{color:red}", 200},
		{"/static/js/bundle-86c5f2802d0bc25d.js", immutable, "console.log(1)", 200},
		{s.URL("js/vendor/lib.min.js"), immutable, "lib", 200},
		{"/static/css/app.00000000.css", "", "", 404},
		{"/static/missing.css", "", "", 404},
		{"/static/css", "", "", 404},
		{"/static/", "", "", 404},
		{"/static/../web.go", "", "", 404},
	}
	for _, c := range cases {
		rr := serve(s, httptest.NewRequest("GET", c.path, nil))
		if rr.Code != c.status || rr.Header().Get("Cache-Control") != c.cache || (c.body != "" && rr.Body.String() != c.body) {
			t.Errorf("GET %s: %d %q %q", c.path, rr.Code, rr.Header().Get("Cache-Control"), rr.Body.String())
		}
	}
	rr := serve(s, httptest.NewRequest("GET", s.URL("img/logo.svg"), nil))
	if ct := rr.Header().Get("Content-Type"); ct != "image/svg+xml" {
		t.Errorf("svg Content-Type = %q", ct)
	}
	if ct := serve(s, httptest.NewRequest("GET", s.URL("css/app.css"), nil)).Header().Get("Content-Type"); ct != "text/css; charset=utf-8" {
		t.Errorf("css Content-Type = %q", ct)
	}
}

func TestStaticRevalidates(t *testing.T) {
	s := newStatic(t)
	rr := serve(s, httptest.NewRequest("GET", "/static/css/app.css", nil))
	etag := rr.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	req := httptest.NewRequest("GET", "/static/css/app.css", nil)
	req.Header.Set("If-None-Match", etag)
	if rr := serve(s, req); rr.Code != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", rr.Code)
	}
	head := serve(s, httptest.NewRequest("HEAD", "/static/css/app.css", nil))
	if head.Code != 200 || head.Body.Len() != 0 {
		t.Errorf("HEAD: %d len=%d", head.Code, head.Body.Len())
	}
	if rr := serve(s, httptest.NewRequest("POST", "/static/css/app.css", nil)); rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST: %d", rr.Code)
	}
}

func TestStaticDevDir(t *testing.T) {
	dir := t.TempDir()
	css := filepath.Join(dir, "css", "app.css")
	if err := os.MkdirAll(filepath.Dir(css), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(css, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := web.NewStatic(staticFS(), web.StaticOptions{DevDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if u := s.URL("css/app.css"); u != "/static/css/app.css" {
		t.Errorf("dev URL = %s", u)
	}
	rr := serve(s, httptest.NewRequest("GET", "/static/css/app.css", nil))
	if rr.Body.String() != "v1" || rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("dev: %q %q", rr.Body.String(), rr.Header().Get("Cache-Control"))
	}
	if err := os.WriteFile(css, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if body := serve(s, httptest.NewRequest("GET", "/static/css/app.css", nil)).Body.String(); body != "v2" {
		t.Errorf("dev mode served stale content %q", body)
	}
	if rr := serve(s, httptest.NewRequest("GET", "/static/css", nil)); rr.Code != 404 {
		t.Errorf("dev directory: %d", rr.Code)
	}
}

func TestStaticNilFS(t *testing.T) {
	if _, err := web.NewStatic(nil, web.StaticOptions{}); err == nil {
		t.Error("nil fs accepted")
	}
}

// 页面和错误页的组件通过 StaticURL 取到带指纹的地址；没有设置 Options.Static 时退回原始地址。
func TestStaticURLInPages(t *testing.T) {
	link := templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := io.WriteString(w, web.StaticURL(ctx, "css/app.css"))
		return err
	})
	s := newStatic(t)
	opts := options()
	opts.Static = s
	opts.ErrorPage = func(int, string) templ.Component { return link }
	p := web.New(opts)
	h := handle(p, func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Path == "/fail" {
			return web.ErrNotFound.New()
		}
		return web.Render(w, r, http.StatusOK, link)
	})
	for _, path := range []string{"/", "/fail"} {
		if body := serve(h, httptest.NewRequest("GET", path, nil)).Body.String(); body != s.URL("css/app.css") {
			t.Errorf("%s rendered %q", path, body)
		}
	}
	nf := httptest.NewRecorder()
	p.NotFound(nf, httptest.NewRequest("GET", "/nope", nil))
	if nf.Body.String() != s.URL("css/app.css") {
		t.Errorf("NotFound rendered %q", nf.Body.String())
	}
	if got := web.StaticURL(context.Background(), "css/app.css"); got != "/static/css/app.css" {
		t.Errorf("StaticURL without Static = %s", got)
	}
}
