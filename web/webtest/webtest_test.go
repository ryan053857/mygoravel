package webtest_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/web"
	"github.com/go-tenon/tenon/web/webtest"
)

func text(format string, args ...any) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := fmt.Fprintf(w, format, args...)
		return err
	})
}

type subscription struct {
	Email string `json:"email" format:"email"`
}

var errLocked = errorx.Define(errorx.Conflict, "auth.forbidden") // 借用内置译文

// app 是一组公开页面：文章列表、订阅表单，以及记住语言的 cookie。
func app() http.Handler {
	pages := web.New(web.Options{
		ErrorPage: func(status int, msg string) templ.Component { return text("error %d %s", status, msg) },
	})
	r := chi.NewRouter()
	r.Use(httpx.Locale(nil))
	site := pages.Routes(r)
	site.Get("/articles", func(w http.ResponseWriter, r *http.Request) error {
		if p := r.URL.Query().Get("page"); p != "" {
			web.PushURL(w, "/articles?page="+p)
		}
		lang := "-"
		if c, err := r.Cookie("lang"); err == nil {
			lang = c.Value
		}
		return web.Render(w, r, http.StatusOK, text("articles lang=%s", lang))
	})
	site.Post("/lang", func(w http.ResponseWriter, r *http.Request) error {
		http.SetCookie(w, &http.Cookie{Name: "lang", Value: r.FormValue("lang"), Path: "/", Secure: true})
		web.Redirect(w, r, "/articles")
		return nil
	})
	site.Post("/subscribe", func(w http.ResponseWriter, r *http.Request) error {
		f, err := web.ParseForm[subscription](r)
		if err != nil {
			return err
		}
		if !f.Valid() {
			return web.Render(w, r, http.StatusUnprocessableEntity, text("invalid email: %s", f.Error("email")))
		}
		w.Header().Set("HX-Trigger", `{"subscribed":{"email":"`+f.Data.Email+`"}}`)
		return web.Render(w, r, http.StatusOK, text("subscribed"))
	})
	site.Delete("/subscription", func(http.ResponseWriter, *http.Request) error { return errLocked.New() })
	return r
}

func TestCookies(t *testing.T) {
	c := webtest.New(t, app())
	if res := c.Get("/articles"); res.Code != http.StatusOK || res.Text() != "articles lang=-" {
		t.Fatalf("before: %d %q", res.Code, res.Text())
	}
	// 响应设置的 cookie（带 Secure）在之后的请求中带上。
	if res := c.Post("/lang", url.Values{"lang": {"en"}}); res.Code != http.StatusSeeOther || res.Redirect() != "/articles" {
		t.Fatalf("set: %d %q", res.Code, res.Redirect())
	}
	if res := c.Get("/articles"); res.Text() != "articles lang=en" {
		t.Fatalf("after: %q", res.Text())
	}
	// htmx 请求的跳转改写为 HX-Redirect。
	if res := c.Post("/lang", url.Values{"lang": {"zh"}}, webtest.HTMX); res.Code != http.StatusNoContent || res.Redirect() != "/articles" {
		t.Fatalf("htmx redirect: %d %q", res.Code, res.Redirect())
	}
	if res := c.Get("/articles?page=2", webtest.HTMX); res.PushURL() != "/articles?page=2" {
		t.Fatalf("push url: %q", res.PushURL())
	}
}

func TestForms(t *testing.T) {
	c := webtest.New(t, app())
	res := c.Post("/subscribe", url.Values{"email": {"nope"}}, webtest.HTMX, webtest.Header("Accept-Language", "zh-CN"))
	if res.Code != http.StatusUnprocessableEntity || res.Text() == "invalid email: " {
		t.Fatalf("invalid: %d %q", res.Code, res.Text())
	}
	res = c.Post("/subscribe", url.Values{"email": {"a@example.com"}}, webtest.HTMX)
	if d, ok := res.Event("subscribed"); res.Code != http.StatusOK || !ok || d["email"] != "a@example.com" {
		t.Fatalf("subscribed: %d %v %v", res.Code, d, ok)
	}
	if _, ok := res.Event("missing"); ok {
		t.Fatal("missing event reported")
	}
	// 局部请求的错误作为提示送达，按请求的语言翻译。
	res = c.Delete("/subscription", webtest.HTMX, webtest.Header("Accept-Language", "en"))
	if msgs := res.Messages(); res.Code != http.StatusConflict || len(msgs) != 1 || msgs[0].Type != "error" || msgs[0].Title == "" {
		t.Fatalf("error: %d %+v", res.Code, msgs)
	}
	if res.Messages() == nil {
		t.Fatal("messages")
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
