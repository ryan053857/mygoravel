package web_test

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
)

// text 是一个输出转义文本的组件。
func text(format string, args ...any) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, html.EscapeString(fmt.Sprintf(format, args...)))
		return err
	})
}

func failing() templ.Component {
	return templ.ComponentFunc(func(context.Context, io.Writer) error { return fmt.Errorf("boom") })
}

// options 返回测试用的完整 Options：内存会话，错误页输出 "page 状态码 消息"，片段输出 "fragment 状态码 消息"。
func options() web.Options {
	var cfg session.Config
	cfg.SetDefaults()
	cfg.Cookie.Secure = false
	return web.Options{
		Sessions:      session.New(cfg, memstore.New()),
		Login:         "/login",
		ErrorPage:     func(status int, msg string) templ.Component { return text("page %d %s", status, msg) },
		ErrorFragment: func(status int, msg string) templ.Component { return text("fragment %d %s", status, msg) },
		ErrorTarget:   "#flash",
	}
}

func pages() *web.Pages { return web.New(options()) }

// handle 把 fn 注册为公开页面路由，匹配任意路径的 GET、POST、PUT。
func handle(p *web.Pages, fn web.HandlerFunc) http.Handler {
	r := chi.NewRouter()
	routes := p.Routes(r, web.Public)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		routes.Method(method, "/*", fn)
	}
	return r
}

func htmx(r *http.Request, partial bool) *http.Request {
	r.Header.Set("HX-Request", "true")
	if partial {
		r.Header.Set("HX-Request-Type", "partial")
	} else {
		r.Header.Set("HX-Request-Type", "full")
	}
	return r
}

// zh 返回带中文 Localizer 的请求。
func zh(t *testing.T, r *http.Request) *http.Request {
	t.Helper()
	b, err := i18n.New("zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	return r.WithContext(i18n.WithLocale(r.Context(), b, "zh-CN"))
}

func serve(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, r)
	return rr
}
