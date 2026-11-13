package httpx_test

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-tenon/tenon/httpx"
)

const immutable = "public, max-age=31536000, immutable"

func spaFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":               {Data: []byte("<!doctype html><div id=root></div>")},
		"assets/index-BjXc12aB.js": {Data: []byte("console.log(1)")},
		"favicon.svg":              {Data: []byte("<svg/>")},
	}
}

func newSPA(t *testing.T, opts httpx.SPAOptions) *httpx.SPA {
	t.Helper()
	s, err := httpx.NewSPA(spaFS(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func spaGet(h http.Handler, method, path, accept string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

const htmlAccept = "text/html,application/xhtml+xml,*/*;q=0.8"

func TestSPAServe(t *testing.T) {
	t.Parallel()
	s := newSPA(t, httpx.SPAOptions{})
	cases := []struct {
		name, path, accept string
		status             int
		cache, body        string
	}{
		{"root", "/", htmlAccept, 200, "no-cache", "<!doctype"},
		{"index", "/index.html", "", 200, "no-cache", "<!doctype"},
		{"hashed asset", "/assets/index-BjXc12aB.js", "*/*", 200, immutable, "console.log"},
		{"public file", "/favicon.svg", "", 200, "no-cache", "<svg/>"},
		{"client route", "/articles/42", htmlAccept, 200, "no-cache", "<!doctype"},
		{"client route with dot", "/reports/2026.11", htmlAccept, 200, "no-cache", "<!doctype"},
		{"fetch of missing path", "/articles/42", "*/*", 404, "", ""},
		{"missing asset", "/assets/old-AAAAAAAA.js", htmlAccept, 404, "", ""},
		{"no accept", "/articles", "", 404, "", ""},
		{"traversal", "/../go.mod", htmlAccept, 200, "no-cache", "<!doctype"},
	}
	for _, c := range cases {
		rr := spaGet(s, http.MethodGet, c.path, c.accept)
		if rr.Code != c.status {
			t.Errorf("%s: status %d, want %d", c.name, rr.Code, c.status)
			continue
		}
		if got := rr.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: Cache-Control %q, want %q", c.name, got, c.cache)
		}
		if c.status == 200 && !strings.HasPrefix(rr.Body.String(), c.body) {
			t.Errorf("%s: body %q", c.name, rr.Body.String())
		}
	}
	if rr := spaGet(s, http.MethodGet, "/articles/42", htmlAccept); rr.Header().Get("Vary") != "Accept" {
		t.Errorf("fallback Vary = %q", rr.Header().Get("Vary"))
	}
	if rr := spaGet(s, http.MethodGet, "/", htmlAccept); rr.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("index Content-Type = %q", rr.Header().Get("Content-Type"))
	}
}

func TestSPAConditionalAndMethods(t *testing.T) {
	t.Parallel()
	s := newSPA(t, httpx.SPAOptions{})
	etag := spaGet(s, http.MethodGet, "/", htmlAccept).Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	req := httptest.NewRequest(http.MethodGet, "/articles/42", nil)
	req.Header.Set("Accept", htmlAccept)
	req.Header.Set("If-None-Match", etag)
	rr := httptest.NewRecorder()
	s.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotModified {
		t.Errorf("fallback with matching ETag: %d, want 304", rr.Code)
	}
	if rr := spaGet(s, http.MethodHead, "/articles/42", htmlAccept); rr.Code != 200 || rr.Body.Len() != 0 {
		t.Errorf("HEAD: %d, body %d bytes", rr.Code, rr.Body.Len())
	}
	if rr := spaGet(s, http.MethodPost, "/articles", htmlAccept); rr.Code != http.StatusMethodNotAllowed || rr.Header().Get("Allow") != "GET, HEAD" {
		t.Errorf("POST: %d Allow=%q", rr.Code, rr.Header().Get("Allow"))
	}
}

func TestSPAFrameAncestors(t *testing.T) {
	t.Parallel()
	s := newSPA(t, httpx.SPAOptions{})
	for path, want := range map[string]string{
		"/":                         "frame-ancestors 'none'",
		"/articles/42":              "frame-ancestors 'none'",
		"/assets/index-BjXc12aB.js": "",
		"/favicon.svg":              "",
	} {
		if got := spaGet(s, http.MethodGet, path, htmlAccept).Header().Get("Content-Security-Policy"); got != want {
			t.Errorf("%s: CSP %q, want %q", path, got, want)
		}
	}
	s = newSPA(t, httpx.SPAOptions{FrameAncestors: []string{"'self'", "https://partner.example.com"}})
	if got := spaGet(s, http.MethodGet, "/", htmlAccept).Header().Get("Content-Security-Policy"); got != "frame-ancestors 'self' https://partner.example.com" {
		t.Errorf("custom CSP %q", got)
	}
}

func TestSPABase(t *testing.T) {
	t.Parallel()
	s := newSPA(t, httpx.SPAOptions{Base: "/app/"})
	for path, want := range map[string]int{
		"/app/":                         200,
		"/app/assets/index-BjXc12aB.js": 200,
		"/app/articles/42":              200,
		"/articles/42":                  404,
		"/assets/index-BjXc12aB.js":     404,
	} {
		if rr := spaGet(s, http.MethodGet, path, htmlAccept); rr.Code != want {
			t.Errorf("%s: %d, want %d", path, rr.Code, want)
		}
	}
}

func TestNewSPAErrors(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		fsys fs.FS
		opts httpx.SPAOptions
	}{
		"no index":       {fstest.MapFS{".gitkeep": {}}, httpx.SPAOptions{}},
		"nil fs":         {nil, httpx.SPAOptions{}},
		"base no slash":  {spaFS(), httpx.SPAOptions{Base: "/app"}},
		"relative base":  {spaFS(), httpx.SPAOptions{Base: "app/"}},
		"bad dev server": {nil, httpx.SPAOptions{DevServer: "localhost:5173"}},
		"empty frame":    {spaFS(), httpx.SPAOptions{FrameAncestors: []string{""}}},
		"frame with ;":   {spaFS(), httpx.SPAOptions{FrameAncestors: []string{"https://a.example; script-src *"}}},
	} {
		if _, err := httpx.NewSPA(c.fsys, c.opts); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestSPADevServer(t *testing.T) {
	t.Parallel()
	vite := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "vite "+r.Method+" "+r.URL.Path)
	}))
	defer vite.Close()
	// 开发模式不读取 fsys，前端还没有构建也可以启动。
	s, err := httpx.NewSPA(nil, httpx.SPAOptions{DevServer: vite.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/@vite/client", "/src/main.tsx", "/articles/42"} {
		rr := spaGet(s, http.MethodGet, path, "")
		if rr.Code != 200 || rr.Body.String() != "vite GET "+path {
			t.Errorf("%s: %d %q", path, rr.Code, rr.Body.String())
		}
	}

	vite.Close()
	if rr := spaGet(s, http.MethodGet, "/", htmlAccept); rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), vite.URL) {
		t.Errorf("dev server down: %d %q", rr.Code, rr.Body.String())
	}
}
