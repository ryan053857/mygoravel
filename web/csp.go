package web

import (
	"context"
	"crypto/rand"
	"net/http"

	"github.com/a-h/templ"
)

// DefaultCSP 返回页面默认的内容安全策略：只执行本站的脚本文件和带有 nonce 的内联脚本，
// 禁止插件和 <base>，禁止页面被嵌入任何网站的框架。
//
// 'unsafe-eval' 是 Alpine 的表达式（x-data、x-on 等）和 htmx 的 hx-on 所需要的，它们在运行时编译执行。
// 样式、图片、字体、iframe 等其他资源不受限制。需要补充时在它之后追加指令，见 Options.CSP。
func DefaultCSP(nonce string) string {
	return "script-src 'self' 'nonce-" + nonce + "' 'unsafe-eval'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
}

// secure 为页面响应设置 Content-Security-Policy 和 Referrer-Policy: same-origin，
// 并把本次的 CSP nonce 写入 templ.WithNonce，ui.Head 和 web.Scripts 输出的脚本据此带上它。
// ctx 中已经有 nonce 时（同一个请求中已经设置过，或应用自己的中间件设置了）原样返回。
func (p *Pages) secure(w http.ResponseWriter, r *http.Request) context.Context {
	ctx := r.Context()
	if templ.GetNonce(ctx) != "" {
		return ctx
	}
	nonce := rand.Text()
	policy := DefaultCSP(nonce)
	if p.opts.CSP != nil {
		policy = p.opts.CSP(nonce)
	}
	h := w.Header()
	if policy != "" {
		h.Set("Content-Security-Policy", policy)
	}
	h.Set("Referrer-Policy", "same-origin")
	return templ.WithNonce(ctx, nonce)
}
