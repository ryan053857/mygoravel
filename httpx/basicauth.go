package httpx

import (
	"context"
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/logging"
)

type basicUserKey struct{}

// BasicAuth 要求请求带 HTTP Basic 认证，用于框架维护的运维页面（例如 queueweb 的队列面板）和内部文档。
// verify 校验用户名和密码，返回 true 时放行，用户名写入 ctx（BasicUser）；通常比较配置中的账号和用 hashing 保存的密码哈希。
//
// 没有凭据或 verify 返回 false 时响应 401 和 WWW-Authenticate: Basic，浏览器弹出登录框；
// verify 返回错误时响应 503，并记录一条 Error 日志。暴力尝试由外层的限流中间件（例如 RateLimit）限制。
//
// 浏览器会在之后的请求中自动带上 Basic 凭据，跨站发来的请求也会带上：使用它的页面要拒绝跨站的非安全请求，
// 例如用 http.CrossOriginProtection 包装。realm 是浏览器登录框中显示的名称，空字符串时为 "Restricted"。
func BasicAuth(realm string, verify func(ctx context.Context, user, password string) (bool, error)) func(http.Handler) http.Handler {
	if verify == nil {
		panic("httpx: BasicAuth requires verify")
	}
	if realm == "" {
		realm = "Restricted"
	}
	challenge := `Basic realm=` + strconv.Quote(realm) + `, charset="UTF-8"`
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if ok {
				valid, err := verify(r.Context(), user, password)
				if err != nil {
					logging.Or(nil).With(logging.AttrComponent, "http").ErrorContext(r.Context(), "basic auth verify failed", "error", err)
					logging.RecordError(trace.SpanFromContext(r.Context()), err)
					WriteProblem(w, http.StatusServiceUnavailable, "authentication unavailable")
					return
				}
				if valid {
					next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), basicUserKey{}, user)))
					return
				}
			}
			w.Header().Set("WWW-Authenticate", challenge)
			WriteProblem(w, http.StatusUnauthorized, "authentication required")
		})
	}
}

// BasicUser 返回 BasicAuth 认证通过的用户名，不在 BasicAuth 之后时为空。用于在日志中记下操作者。
func BasicUser(ctx context.Context) string {
	user, _ := ctx.Value(basicUserKey{}).(string)
	return user
}
