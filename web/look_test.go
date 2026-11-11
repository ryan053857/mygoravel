package web_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/web"
)

// 没有 ErrorFragment 时，局部请求的错误不替换页面内容，作为提示送达，并与会话中的提示合并为一个事件。
func TestPartialErrorsAsFlash(t *testing.T) {
	o := options()
	o.ErrorFragment, o.ErrorTarget = nil, ""
	h := handle(web.New(o), func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Query().Has("flash") {
			web.Flash(r.Context(), web.Message{Type: "success", Title: "已保存"})
		}
		return errLocked.New()
	})

	rr := serve(h, zh(t, htmx(httptest.NewRequest("POST", "/?flash", nil), true)))
	if rr.Code != 409 || rr.Body.Len() != 0 || rr.Header().Get("HX-Reswap") != "none" || rr.Header().Get("HX-Retarget") != "" {
		t.Fatalf("partial: %d %q %v", rr.Code, rr.Body.String(), rr.Header())
	}
	msgs := flashTrigger(t, rr)
	if len(msgs) != 2 || msgs[0].Title != "已保存" || msgs[1] != (web.Message{Type: "error", Title: "没有访问权限"}) {
		t.Fatalf("messages = %+v (HX-Trigger %s)", msgs, rr.Header().Get("HX-Trigger"))
	}

	// 整页请求仍然渲染 ErrorPage。
	rr = serve(h, htmx(httptest.NewRequest("POST", "/", nil), false))
	if rr.Code != 409 || !strings.HasPrefix(rr.Body.String(), "page 409") {
		t.Fatalf("full: %d %q", rr.Code, rr.Body.String())
	}
}

// 页面处理器之外的错误（这里是跨站请求被拒绝）同样作为提示送达。
func TestPartialErrorsAsFlashOutsideHandler(t *testing.T) {
	o := options()
	o.ErrorFragment, o.ErrorTarget = nil, ""
	h := handle(web.New(o), func(http.ResponseWriter, *http.Request) error { return nil })
	req := htmx(httptest.NewRequest("POST", "http://example.com/", nil), true)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rr := serve(h, req)
	if rr.Code != 403 || rr.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("cross origin: %d %v", rr.Code, rr.Header())
	}
	if msgs := flashTrigger(t, rr); len(msgs) != 1 || msgs[0].Type != "error" {
		t.Fatalf("messages = %+v", msgs)
	}
}

// Login 中的 {name} 按当前请求的路径参数替换。
func TestLoginPlaceholders(t *testing.T) {
	o := options()
	o.Login = "/t/{tenant}/login"
	pages := web.New(o)
	router := chi.NewRouter()
	pages.Routes(router, web.Require()).Get("/t/{tenant}/dash", func(http.ResponseWriter, *http.Request) error { return nil })

	rr := serve(router, httptest.NewRequest("GET", "/t/acme%20co/dash", nil))
	if loc := rr.Header().Get("Location"); rr.Code != 303 || loc != "/t/acme%20co/login?next=%2Ft%2Facme%2520co%2Fdash" {
		t.Fatalf("redirect: %d %q", rr.Code, loc)
	}
}

// themeName 输出 ThemeFrom 中的风格。
func themeName(prefix string) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, "%s %s", prefix, web.ThemeFrom(ctx).Style)
		return err
	})
}

// 每个 Pages 的页面和错误页都能读到自己的 Theme。
func TestThemeFrom(t *testing.T) {
	router := chi.NewRouter()
	for _, area := range []struct{ prefix, style string }{{"/ops", "nova"}, {"/app", "maia"}} {
		o := options()
		o.Theme = web.Theme{Style: area.style}
		o.ErrorPage = func(status int, _ string) templ.Component { return themeName(fmt.Sprint(status)) }
		pages := web.New(o)
		router.Route(area.prefix, func(r chi.Router) {
			r.NotFound(pages.NotFound)
			pages.Routes(r, web.Public).Get("/", func(w http.ResponseWriter, r *http.Request) error {
				return web.Render(w, r, http.StatusOK, themeName("page"))
			})
		})
	}
	for path, want := range map[string]string{"/ops/": "page nova", "/app/": "page maia", "/ops/nope": "404 nova", "/app/nope": "404 maia"} {
		if got := serve(router, httptest.NewRequest("GET", path, nil)).Body.String(); got != want {
			t.Errorf("%s = %q, want %q", path, got, want)
		}
	}
	if s := web.ThemeFrom(context.Background()).Style; s != "" {
		t.Errorf("outside pages: %q", s)
	}
}

func TestThemeValidate(t *testing.T) {
	ok := web.Theme{Style: "maia", Mode: "dark", Radius: "1rem", Font: `"Inter", sans-serif`, Vars: map[string]string{"primary": "oklch(0.5 0.2 275)"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, th := range map[string]web.Theme{
		"style":     {Style: "fancy"},
		"mode":      {Mode: "auto"},
		"radius":    {Radius: "1rem;}"},
		"var name":  {Vars: map[string]string{"--primary": "red"}},
		"var value": {DarkVars: map[string]string{"primary": "red</style>"}},
	} {
		if th.Validate() == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestCloseDialog(t *testing.T) {
	rr := httptest.NewRecorder()
	web.CloseDialog(rr, "article-dialog")
	if got := rr.Header().Get("HX-Trigger"); got != `{"web:dialog-close":{"id":"article-dialog","target":"body"}}` {
		t.Fatalf("HX-Trigger = %s", got)
	}
}
