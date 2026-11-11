package webtest_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/webtest"
)

func text(format string, args ...any) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	})
}

// flashes 渲染会话中的提示，模拟布局。
var flashes = templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
	for _, m := range web.Flashes(ctx) {
		fmt.Fprintf(w, "[%s:%s]", m.Type, m.Title)
	}
	return nil
})

type changes struct {
	Title string `json:"title" minLength:"1"`
}

var errLocked = errorx.Define(errorx.Conflict, "auth.forbidden") // 借用内置译文

// pages 返回一组页面，会话 cookie 名为 cookie，登录页为 prefix/login。
func pages(cookie, prefix string) *web.Pages {
	var cfg session.Config
	cfg.SetDefaults() // 默认 Secure
	cfg.Cookie.Name = cookie
	return web.New(web.Options{
		Sessions:  session.New(cfg, memstore.New()),
		Login:     prefix + "/login",
		ErrorPage: func(status int, msg string) templ.Component { return text("error %d %s", status, msg) },
	})
}

// app 是两组页面：/ops（需要 articles 权限）和 /app。
func app() (http.Handler, *web.Pages, *web.Pages) {
	ops, tenant := pages("ops_session", "/ops"), pages("app_session", "/app")
	r := chi.NewRouter()
	r.Use(httpx.Locale(nil))
	r.Route("/ops", func(r chi.Router) {
		editor := ops.Routes(r, web.Require("articles"))
		editor.Get("/articles", func(w http.ResponseWriter, r *http.Request) error {
			sub, _ := identity.FromContext(r.Context())
			if r.URL.Query().Get("page") != "" {
				web.PushURL(w, "/ops/articles?page="+r.URL.Query().Get("page"))
			}
			return web.Render(w, r, http.StatusOK, templ.Join(text("articles for %s ", sub.ID), flashes))
		})
		editor.Put("/articles/{id}", func(w http.ResponseWriter, r *http.Request) error {
			f, err := web.ParseForm[changes](r)
			if err != nil {
				return err
			}
			if !f.Valid() {
				return web.Render(w, r, http.StatusUnprocessableEntity, text("invalid title: %s", f.Error("title")))
			}
			web.Flash(r.Context(), web.Message{Type: "success", Title: "已保存"})
			if r.URL.Query().Has("dialog") {
				web.CloseDialog(w, "article-dialog")
				web.Trigger(w, "articles:changed", map[string]any{"id": chi.URLParam(r, "id")})
				w.WriteHeader(http.StatusNoContent)
				return nil
			}
			web.Redirect(w, r, "/ops/articles")
			return nil
		})
		editor.Delete("/articles/{id}", func(http.ResponseWriter, *http.Request) error { return errLocked.New() })
	})
	r.Route("/app", func(r chi.Router) {
		tenant.Routes(r, web.Require()).Get("/", func(w http.ResponseWriter, r *http.Request) error {
			sub, _ := identity.FromContext(r.Context())
			return web.Render(w, r, http.StatusOK, text("home of %s", sub.ID))
		})
	})
	return r, ops, tenant
}

var editor = identity.Subject{ID: "alice", Type: identity.SubjectUser, Permissions: []string{"articles"}}

func TestLogin(t *testing.T) {
	h, ops, tenant := app()
	c := webtest.New(t, h)

	// 未登录：整页请求 303，htmx 请求 HX-Redirect。
	if res := c.Get("/ops/articles"); res.Code != http.StatusSeeOther || res.Redirect() != "/ops/login?next=%2Fops%2Farticles" {
		t.Fatalf("anonymous: %d %q", res.Code, res.Redirect())
	}
	if res := c.Get("/ops/articles", webtest.HTMX); res.Code != http.StatusNoContent || !strings.HasPrefix(res.Redirect(), "/ops/login") {
		t.Fatalf("anonymous htmx: %d %q", res.Code, res.Redirect())
	}

	c.Login(ops, editor)
	if res := c.Get("/ops/articles"); res.Code != http.StatusOK || res.Text() != "articles for alice " {
		t.Fatalf("logged in: %d %q", res.Code, res.Text())
	}
	// 两组页面的会话互相独立：登录运营后台不会登录租户端。
	if res := c.Get("/app/"); res.Code != http.StatusSeeOther {
		t.Fatalf("other area: %d", res.Code)
	}
	c.Login(tenant, identity.Subject{ID: "bob"})
	if res := c.Get("/app/"); res.Text() != "home of bob" {
		t.Fatalf("tenant: %q", res.Text())
	}

	// 权限不足的主体得到 403。
	guest := webtest.New(t, h)
	guest.Login(ops, identity.Subject{ID: "carol"})
	if res := guest.Get("/ops/articles"); res.Code != http.StatusForbidden {
		t.Fatalf("missing permission: %d", res.Code)
	}
}

func TestForms(t *testing.T) {
	h, ops, _ := app()
	c := webtest.New(t, h)
	c.Login(ops, editor)

	// 验证失败：422 和错误提示，按请求的语言翻译。
	res := c.Put("/ops/articles/1", url.Values{"title": {""}}, webtest.HTMX, webtest.Header("Accept-Language", "zh-CN"))
	if res.Code != http.StatusUnprocessableEntity || !strings.HasPrefix(res.Text(), "invalid title: ") {
		t.Fatalf("invalid: %d %q", res.Code, res.Text())
	}

	// 保存后跳转：提示留在会话中，由跳转后的页面显示一次。
	res = c.Put("/ops/articles/1", url.Values{"title": {"Hello"}}, webtest.HTMX)
	if res.Redirect() != "/ops/articles" || res.Flashes() != nil {
		t.Fatalf("saved: %d %q %v", res.Code, res.Redirect(), res.Flashes())
	}
	if page := c.Get(res.Redirect()); page.Text() != "articles for alice [success:已保存]" {
		t.Fatalf("next page: %q", page.Text())
	}
	if page := c.Get("/ops/articles"); page.Text() != "articles for alice " {
		t.Fatalf("flash shown twice: %q", page.Text())
	}

	// 对话框中保存：204，提示随响应送达，关闭对话框并触发刷新列表的事件。
	res = c.Put("/ops/articles/7?dialog", url.Values{"title": {"Hello"}}, webtest.HTMX)
	if res.Code != http.StatusNoContent || !slices.Equal(res.Flashes(), []web.Message{{Type: "success", Title: "已保存"}}) {
		t.Fatalf("dialog: %d %v", res.Code, res.Flashes())
	}
	if d, ok := res.Event(web.DialogCloseEvent); !ok || d["id"] != "article-dialog" {
		t.Fatalf("dialog close: %v %v", d, ok)
	}
	if d, ok := res.Event("articles:changed"); !ok || d["id"] != "7" {
		t.Fatalf("articles:changed: %v %v", d, ok)
	}
	if _, ok := res.Event("missing"); ok {
		t.Fatal("missing event reported")
	}

	// 局部请求的错误作为 error 提示送达。
	res = c.Delete("/ops/articles/1", webtest.HTMX, webtest.Header("Accept-Language", "en"))
	if res.Code != http.StatusConflict || !slices.Equal(res.Flashes(), []web.Message{{Type: "error", Title: "Permission denied"}}) {
		t.Fatalf("error: %d %v", res.Code, res.Flashes())
	}
	// 整页 htmx 请求的错误渲染错误页。
	if res := c.Delete("/ops/articles/1", webtest.HTMXPage); !strings.HasPrefix(res.Text(), "error 409") {
		t.Fatalf("full page error: %q", res.Text())
	}

	if res := c.Get("/ops/articles?page=2", webtest.HTMX); res.PushURL() != "/ops/articles?page=2" {
		t.Fatalf("push url: %q", res.PushURL())
	}
}

func TestEventWithoutObjectDetail(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("HX-Trigger", `{"saved":"文章","done":{"n":1}}`)
	})
	res := webtest.New(t, h).Get("/")
	if d, ok := res.Event("saved"); !ok || d["value"] != "文章" {
		t.Fatalf("saved: %v %v", d, ok)
	}
	if d, _ := res.Event("done"); d["n"] != float64(1) {
		t.Fatalf("done: %v", d)
	}
	h = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Header().Set("HX-Trigger", "saved, done") })
	if _, ok := webtest.New(t, h).Get("/").Event("done"); !ok {
		t.Fatal("comma separated events")
	}
}
