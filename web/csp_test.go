package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/go-tenon/tenon/web"
)

// TestPagesSetCSP 检查页面路由和路由之外的错误页都带有内容安全策略，且 nonce 与 templ context 中的一致。
func TestPagesSetCSP(t *testing.T) {
	t.Parallel()
	var nonce string
	h := handle(pages(), func(w http.ResponseWriter, r *http.Request) error {
		nonce = templ.GetNonce(r.Context())
		_, err := io.WriteString(w, "ok")
		return err
	})
	rr := serve(h, httptest.NewRequest("GET", "/", nil))
	if nonce == "" {
		t.Fatal("no nonce in context")
	}
	if got, want := rr.Header().Get("Content-Security-Policy"), web.DefaultCSP(nonce); got != want {
		t.Errorf("CSP %q, want %q", got, want)
	}
	if got := rr.Header().Get("Referrer-Policy"); got != "same-origin" {
		t.Errorf("Referrer-Policy %q", got)
	}
	again := serve(h, httptest.NewRequest("GET", "/", nil))
	if again.Header().Get("Content-Security-Policy") == rr.Header().Get("Content-Security-Policy") {
		t.Error("nonce reused across requests")
	}

	nf := serve(http.HandlerFunc(pages().NotFound), httptest.NewRequest("GET", "/nope", nil))
	if csp := nf.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'none'") || !strings.Contains(csp, "'nonce-") {
		t.Errorf("NotFound CSP %q", csp)
	}
}

func TestPagesCustomCSP(t *testing.T) {
	t.Parallel()
	opts := options()
	opts.CSP = func(nonce string) string { return "script-src 'nonce-" + nonce + "'" }
	var nonce string
	h := handle(web.New(opts), func(_ http.ResponseWriter, r *http.Request) error {
		nonce = templ.GetNonce(r.Context())
		return nil
	})
	if got := serve(h, httptest.NewRequest("GET", "/", nil)).Header().Get("Content-Security-Policy"); got != "script-src 'nonce-"+nonce+"'" {
		t.Errorf("custom CSP %q", got)
	}

	opts.CSP = func(string) string { return "" }
	rr := serve(handle(web.New(opts), func(http.ResponseWriter, *http.Request) error { return nil }), httptest.NewRequest("GET", "/", nil))
	if _, ok := rr.Header()["Content-Security-Policy"]; ok {
		t.Errorf("empty CSP still set: %v", rr.Header())
	}
}

// TestPagesKeepAppNonce 检查应用自己的中间件已经设置了 nonce 时，Pages 不再覆盖它。
func TestPagesKeepAppNonce(t *testing.T) {
	t.Parallel()
	var nonce string
	h := handle(pages(), func(_ http.ResponseWriter, r *http.Request) error {
		nonce = templ.GetNonce(r.Context())
		return nil
	})
	r := httptest.NewRequest("GET", "/", nil)
	rr := serve(h, r.WithContext(templ.WithNonce(r.Context(), "app")))
	if nonce != "app" || rr.Header().Get("Content-Security-Policy") != "" {
		t.Errorf("nonce %q, CSP %q", nonce, rr.Header().Get("Content-Security-Policy"))
	}
}
