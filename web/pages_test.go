package web_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/web"
)

var errLocked = errorx.Define(errorx.Conflict, "auth.forbidden") // 借用内置译文

func TestNewRequiresAllOptions(t *testing.T) {
	for name, mutate := range map[string]func(*web.Options){
		"error page":   func(o *web.Options) { o.ErrorPage = nil },
		"error target": func(o *web.Options) { o.ErrorTarget = "" },
	} {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("expected panic")
				}
			}()
			o := options()
			mutate(&o)
			web.New(o)
		})
	}
}

func TestHandleErrors(t *testing.T) {
	var logs bytes.Buffer
	o := options()
	o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	p := web.New(o)
	conflict := handle(p, func(http.ResponseWriter, *http.Request) error { return errLocked.New() })
	internal := handle(p, func(http.ResponseWriter, *http.Request) error { return errorx.Define(errorx.Internal, "x.y").New() })

	rr := serve(conflict, zh(t, httptest.NewRequest("GET", "/", nil)))
	if rr.Code != 409 || rr.Body.String() != "page 409 没有访问权限" {
		t.Fatalf("page: %d %q", rr.Code, rr.Body.String())
	}
	rr = serve(conflict, htmx(httptest.NewRequest("POST", "/", nil), false))
	if rr.Code != 409 || !strings.HasPrefix(rr.Body.String(), "page 409") || rr.Header().Get("HX-Retarget") != "" {
		t.Fatalf("full htmx: %d %q %v", rr.Code, rr.Body.String(), rr.Header())
	}
	rr = serve(conflict, htmx(httptest.NewRequest("POST", "/", nil), true))
	if rr.Code != 409 || rr.Body.String() != "fragment 409 Conflict" || rr.Header().Get("HX-Retarget") != "#flash" || rr.Header().Get("HX-Reswap") != "innerHTML" {
		t.Fatalf("partial: %d %q %v", rr.Code, rr.Body.String(), rr.Header())
	}
	if logs.Len() != 0 {
		t.Fatalf("4xx logged: %s", logs.String())
	}
	rr = serve(internal, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 500 || rr.Body.String() != "page 500 Internal Server Error" || !strings.Contains(logs.String(), "page handler failed") {
		t.Fatalf("5xx: %d %q logs=%s", rr.Code, rr.Body.String(), logs.String())
	}
}

func TestHandleErrorAfterResponseStarted(t *testing.T) {
	var logs bytes.Buffer
	o := options()
	o.Logger = slog.New(slog.NewTextHandler(&logs, nil))
	p := web.New(o)
	h := handle(p, func(w http.ResponseWriter, r *http.Request) error {
		_, _ = w.Write([]byte("partial"))
		return errorx.Define(errorx.Internal, "x.y").New()
	})
	rr := serve(h, httptest.NewRequest("GET", "/", nil))
	if rr.Body.String() != "partial" || !strings.Contains(logs.String(), "after response started") {
		t.Fatalf("%q logs=%s", rr.Body.String(), logs.String())
	}
}

func TestHandleRewritesRedirectsForHTMX(t *testing.T) {
	h := handle(pages(), func(w http.ResponseWriter, r *http.Request) error {
		http.Redirect(w, r, "/articles", http.StatusSeeOther)
		return nil
	})
	rr := serve(h, htmx(httptest.NewRequest("POST", "/", nil), true))
	if rr.Code != 204 || rr.Header().Get("HX-Redirect") != "/articles" || rr.Header().Get("Location") != "" {
		t.Fatalf("htmx: %d %v", rr.Code, rr.Header())
	}
	rr = serve(h, httptest.NewRequest("POST", "/", nil))
	if rr.Code != 303 || rr.Header().Get("Location") != "/articles" {
		t.Fatalf("plain: %d %v", rr.Code, rr.Header())
	}
}

func TestNotFound(t *testing.T) {
	rr := serve(http.HandlerFunc(pages().NotFound), zh(t, httptest.NewRequest("GET", "/nope", nil)))
	if rr.Code != 404 || rr.Body.String() != "page 404 页面不存在" {
		t.Fatalf("%d %q", rr.Code, rr.Body.String())
	}
}

func TestErrorSetsRetryAfter(t *testing.T) {
	h := handle(pages(), func(http.ResponseWriter, *http.Request) error {
		return errorx.Define(errorx.ResourceExhausted, "rate_limit.exceeded").New(errorx.RetryAfter(1500 * time.Millisecond))
	})
	rr := serve(h, httptest.NewRequest("POST", "/login", nil))
	if rr.Code != 429 || rr.Header().Get("Retry-After") != "2" {
		t.Fatalf("%d %v", rr.Code, rr.Header())
	}
}
