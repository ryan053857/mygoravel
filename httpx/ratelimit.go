package httpx

import (
	"log/slog"
	"math"
	"net"
	"net/http"
	"strconv"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/ratelimit"
)

// KeyFunc 返回请求的限流 key；返回空字符串表示该请求不限流。
type KeyFunc func(*http.Request) string

// KeyByIP 以客户端地址（不含端口）为 key。
// 部署在代理之后时，需要在它之前安装 ClientIP。
func KeyByIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// RateLimit 按 key 返回的值调用 limiter，配额用完时返回 429 并设置 Retry-After。
// limiter 返回其他错误时返回 503，错误写入访问日志的 error 字段。
// limiter 或 key 为 nil 时 panic。
func RateLimit(limiter ratelimit.Limiter, key KeyFunc) func(http.Handler) http.Handler {
	if limiter == nil {
		panic("httpx: RateLimit: nil limiter")
	}
	if key == nil {
		panic("httpx: RateLimit: nil key function")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k == "" {
				next.ServeHTTP(w, r)
				return
			}
			switch err := limiter.Allow(r.Context(), k); {
			case err == nil:
				next.ServeHTTP(w, r)
			case errors.Is(err, ratelimit.ErrExceeded):
				retry := math.Ceil(errorx.RetryAfterOf(err).Seconds())
				w.Header().Set("Retry-After", strconv.Itoa(int(retry)))
				WriteProblem(w, http.StatusTooManyRequests, "too many requests")
			default:
				AddAccessLogAttrs(r.Context(), slog.String("error", err.Error()))
				WriteProblem(w, http.StatusServiceUnavailable, "rate limiter unavailable")
			}
		})
	}
}
