package app

import (
	"crypto/rand"
	"io"
	"net/http"
	"os"
	"sync"

	"github.com/a-h/templ"
)

// cspPolicy 与视图文档「内容安全策略」一节相同，另加 report-uri 收集违规。
// POC_CSP_NO_EVAL=1 时去掉 'unsafe-eval'，用来确认 Alpine 与 htmx 的 hx-on 确实需要它。
func cspPolicy(nonce string) string {
	eval := " 'unsafe-eval'"
	if os.Getenv("POC_CSP_NO_EVAL") != "" {
		eval = ""
	}
	return "script-src 'self' 'nonce-" + nonce + "'" + eval + "; object-src 'none'; base-uri 'none'; report-uri /csp-report"
}

// csp 为每个请求生成 nonce，写入 CSP 响应头和 templ 的 context。
func csp(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nonce := rand.Text()
		w.Header().Set("Content-Security-Policy", cspPolicy(nonce))
		next.ServeHTTP(w, r.WithContext(templ.WithNonce(r.Context(), nonce)))
	})
}

var (
	reportsMu sync.Mutex
	reports   []string
)

// CSPViolations 返回并清空浏览器上报的违规。
func CSPViolations() []string {
	reportsMu.Lock()
	defer reportsMu.Unlock()
	r := reports
	reports = nil
	return r
}

func cspReport(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	reportsMu.Lock()
	reports = append(reports, string(b))
	reportsMu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}
