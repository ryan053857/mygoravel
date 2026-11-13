package web

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/a-h/templ"
)

// IsHTMX 报告请求是否由 htmx 发出（带 HX-Request: true）。
func IsHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// IsPartial 报告请求只需要页面片段：htmx 请求的目标不是 body，也没有 hx-select。
// 普通请求、后退时 htmx 重新请求的整页、目标为 body 的请求都返回 false。
func IsPartial(r *http.Request) bool {
	return IsHTMX(r) && r.Header.Get("HX-Request-Type") == "partial"
}

// Render 渲染组件并以 status 写出 HTML。组件先渲染到缓冲区，渲染失败时不写出任何内容。
// 组件使用 r.Context() 渲染，因此可以在组件中用 i18n.T 翻译、用 identity.FromContext 读取主体。
func Render(w http.ResponseWriter, r *http.Request, status int, c templ.Component) error {
	var buf bytes.Buffer
	if err := c.Render(r.Context(), &buf); err != nil {
		return err
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	// 同一个地址可能按 HX-Request 返回整页或片段，缓存必须区分。
	h.Add("Vary", "HX-Request")
	w.WriteHeader(status)
	_, err := w.Write(buf.Bytes())
	return err
}

// Redirect 跳转到 url：htmx 请求写 HX-Redirect 响应头，由浏览器整页跳转；普通请求返回 303。
// Pages.Handle 中的处理器也可以直接用 http.Redirect，3xx 会被同样改写。
func Redirect(w http.ResponseWriter, r *http.Request, url string) {
	if IsHTMX(r) {
		w.Header().Set("HX-Redirect", url)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, url, http.StatusSeeOther)
}

// LocalPath 在 path 是本站路径（以单个 / 开头）时返回它，否则返回 fallback。
// 用于校验 next 参数等来自请求的跳转地址，防止跳到其他站点。
func LocalPath(path, fallback string) string {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.HasPrefix(path, `/\`) {
		return fallback
	}
	return path
}
