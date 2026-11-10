package web

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/rest"
)

// HandlerFunc 是返回错误的页面处理器。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Errors 决定处理器错误如何呈现。
type Errors struct {
	// Page 渲染整页错误，用于普通请求和整页 htmx 请求。
	Page func(status int, err error) templ.Component
	// Fragment 渲染片段错误，用于局部 htmx 请求；它被替换到 Target。
	Fragment func(status int, err error) templ.Component
	// Target 是片段错误的替换位置，例如 #flash。
	Target string
	Logger *slog.Logger
}

// Handle 把 fn 适配为 http.Handler：fn 返回的错误按 errorx 分类映射状态码。
// 局部 htmx 请求通过 HX-Retarget 把错误片段放进 Target，其余请求渲染整页错误。
func (e Errors) Handle(fn HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := fn(w, r)
		if err == nil {
			return
		}
		status := rest.HTTPStatus(err)
		if status >= 500 {
			e.Logger.ErrorContext(r.Context(), "page handler failed", "error", err, "kind", errorx.KindOf(err).String())
		}
		var c templ.Component
		if IsPartial(r) {
			w.Header().Set("HX-Retarget", e.Target)
			w.Header().Set("HX-Reswap", "innerHTML")
			c = e.Fragment(status, err)
		} else {
			c = e.Page(status, err)
		}
		if rerr := Render(w, r, status, c); rerr != nil {
			e.Logger.ErrorContext(r.Context(), "render error page failed", "error", rerr)
			http.Error(w, http.StatusText(status), status)
		}
	})
}
