package web_test

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryan053857/mygoravel/httpx/web"
	"github.com/ryan053857/mygoravel/i18n"
)

// text 是一个输出转义文本的组件。
func text(format string, args ...any) web.Component {
	return web.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, html.EscapeString(fmt.Sprintf(format, args...)))
		return err
	})
}

func failing() web.Component {
	return web.ComponentFunc(func(context.Context, io.Writer) error { return fmt.Errorf("boom") })
}

func pages() *web.Pages {
	return web.New(web.Options{
		ErrorPage:     func(status int, msg string) web.Component { return text("page %d %s", status, msg) },
		ErrorFragment: func(status int, msg string) web.Component { return text("fragment %d %s", status, msg) },
		ErrorTarget:   "#flash",
	})
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
