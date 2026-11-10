// Package web 是 httpx/web 的原型：用 templ 渲染页面，配合 htmx 4 与 Alpine.js。
package web

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
)

// IsHTMX 报告请求是否由 htmx 发出。
func IsHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// IsPartial 报告 htmx 请求只需要片段：目标不是 body 且没有 hx-select。
// 普通请求、后退时重新请求的整页、目标为 body 的请求都返回 false。
func IsPartial(r *http.Request) bool {
	return IsHTMX(r) && r.Header.Get("HX-Request-Type") == "partial"
}

// Render 先把组件渲染到缓冲区，成功后再写出 status 和 HTML；渲染失败时什么都不写出。
func Render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) error {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// 同一个 URL 会按 HX-Request 返回整页或片段，缓存必须区分。
	h.Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, err := w.Write(buf.Bytes())
	return err
}

// Redirect 跳转到 url：htmx 请求写 HX-Redirect 由浏览器整页跳转，普通请求返回 303。
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if IsHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}
