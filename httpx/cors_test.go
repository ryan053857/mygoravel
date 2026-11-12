package httpx_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/httpx"
)

func corsHandler(t *testing.T, origins ...string) (http.Handler, *int) {
	t.Helper()
	cfg := httpx.DefaultCORSConfig()
	cfg.AllowedOrigins = origins
	cfg.ExposeHeaders = []string{"X-Total-Count"}
	calls := new(int)
	h := httpx.CORS(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls++
		if r.URL.Path == "/private" {
			http.Error(w, "unauthenticated", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	return h, calls
}

func preflight(h http.Handler, origin string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodOptions, "/articles", nil)
	req.Header.Set("Origin", origin)
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	req.Header.Set("Access-Control-Request-Headers", "authorization, content-type, x-tenant")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestCORSPreflight(t *testing.T) {
	t.Parallel()
	h, calls := corsHandler(t, "https://app.example.com")
	rr := preflight(h, "https://app.example.com")
	hd := rr.Header()
	if rr.Code != http.StatusNoContent || *calls != 0 {
		t.Fatalf("status %d, handler called %d times", rr.Code, *calls)
	}
	if hd.Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		!strings.Contains(strings.ToLower(hd.Get("Access-Control-Allow-Headers")), "authorization") ||
		!strings.Contains(hd.Get("Access-Control-Allow-Methods"), http.MethodPost) ||
		hd.Get("Access-Control-Max-Age") != "7200" {
		t.Fatalf("preflight headers %v", hd)
	}
	if hd.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatal("credentials allowed")
	}

	// 不在列表中的来源得不到 CORS 头，浏览器会拦截。
	rr = preflight(h, "https://evil.example.net")
	if rr.Header().Get("Access-Control-Allow-Origin") != "" || *calls != 0 {
		t.Fatalf("disallowed origin %v", rr.Header())
	}
}

func TestCORSRequests(t *testing.T) {
	t.Parallel()
	h, calls := corsHandler(t, "https://*.example.com", "capacitor://localhost")
	for _, tc := range []struct {
		origin, path string
		allowed      bool
		status       int
	}{
		{"https://app.example.com", "/articles", true, http.StatusOK},
		{"capacitor://localhost", "/articles", true, http.StatusOK},
		{"https://app.example.com", "/private", true, http.StatusUnauthorized}, // 错误响应也带 CORS 头
		{"https://example.com", "/articles", false, http.StatusOK},
		{"https://app.example.com.evil.net", "/articles", false, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Origin", tc.origin)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		got := rr.Header().Get("Access-Control-Allow-Origin")
		if rr.Code != tc.status || (got == tc.origin) != tc.allowed {
			t.Errorf("%s %s: status %d, allow-origin %q", tc.origin, tc.path, rr.Code, got)
		}
		if tc.allowed {
			exposed := rr.Header().Get("Access-Control-Expose-Headers")
			for _, name := range []string{"X-Request-Id", "Retry-After", "Location", "X-Total-Count"} {
				if !strings.Contains(strings.ToLower(exposed), strings.ToLower(name)) {
					t.Errorf("%s not exposed: %q", name, exposed)
				}
			}
		}
	}
	if *calls != 5 {
		t.Fatalf("handler called %d times", *calls)
	}
}

func TestCORSAnyOrigin(t *testing.T) {
	t.Parallel()
	h, _ := corsHandler(t, "*")
	rr := preflight(h, "https://anywhere.example.org")
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" || rr.Header().Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("headers %v", rr.Header())
	}
}

func TestCORSConfigValidate(t *testing.T) {
	t.Parallel()
	if err := httpx.DefaultCORSConfig().Validate(); err != nil {
		t.Fatalf("default: %v", err)
	}
	for _, origins := range [][]string{
		{"*"}, {"https://app.example.com"}, {"http://localhost:5173"}, {"https://*.example.com"}, {"capacitor://localhost", "wails://wails"},
	} {
		cfg := httpx.DefaultCORSConfig()
		cfg.AllowedOrigins = origins
		if err := cfg.Validate(); err != nil {
			t.Errorf("%v: %v", origins, err)
		}
	}
	for _, origins := range [][]string{
		{}, {""}, {"app.example.com"}, {"https://app.example.com/path"}, {"https://*.*.example.com"}, {"https://app.example.com?x=1"},
	} {
		cfg := httpx.DefaultCORSConfig()
		cfg.AllowedOrigins = origins
		if cfg.Validate() == nil {
			t.Errorf("%q accepted", origins)
		}
	}
	cfg := httpx.DefaultCORSConfig()
	cfg.ExposeHeaders = []string{"Bad Header"}
	if cfg.Validate() == nil {
		t.Error("invalid expose header accepted")
	}
	cfg = httpx.DefaultCORSConfig()
	cfg.MaxAge = 0
	if cfg.Validate() == nil {
		t.Error("zero max_age accepted")
	}
	cfg.MaxAge = time.Minute
	if cfg.Validate() != nil {
		t.Error("positive max_age rejected")
	}
}
