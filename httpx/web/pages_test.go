package web_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/web"
)

var errLocked = errorx.Define(errorx.Conflict, "auth.forbidden") // 借用内置译文

func TestNewRequiresErrorComponents(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	web.New(web.Options{ErrorPage: func(int, string) templ.Component { return text("") }})
}

func TestHandleErrors(t *testing.T) {
	var logs bytes.Buffer
	p := web.New(web.Options{
		ErrorPage:     func(status int, msg string) templ.Component { return text("page %d %s", status, msg) },
		ErrorFragment: func(status int, msg string) templ.Component { return text("fragment %d %s", status, msg) },
		ErrorTarget:   "#flash",
		Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
	})
	conflict := p.Handle(func(http.ResponseWriter, *http.Request) error { return errLocked.New() })
	internal := p.Handle(func(http.ResponseWriter, *http.Request) error { return errorx.Define(errorx.Internal, "x.y").New() })

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
	p := web.New(web.Options{
		ErrorPage:     func(int, string) templ.Component { return text("error page") },
		ErrorFragment: func(int, string) templ.Component { return text("error fragment") },
		ErrorTarget:   "#flash",
		Logger:        slog.New(slog.NewTextHandler(&logs, nil)),
	})
	h := p.Handle(func(w http.ResponseWriter, r *http.Request) error {
		_, _ = w.Write([]byte("partial"))
		return errorx.Define(errorx.Internal, "x.y").New()
	})
	rr := serve(h, httptest.NewRequest("GET", "/", nil))
	if rr.Body.String() != "partial" || !strings.Contains(logs.String(), "after response started") {
		t.Fatalf("%q logs=%s", rr.Body.String(), logs.String())
	}
}

func TestHandleRewritesRedirectsForHTMX(t *testing.T) {
	h := pages().Handle(func(w http.ResponseWriter, r *http.Request) error {
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
