package httpx

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5/middleware"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ryan053857/mygoravel/acquire"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/logging"
)

// Defaults 返回每个应用都应安装的基础中间件，按执行顺序排列：
// AssignRequestID、NoSniff、ClientIP、Locale、AccessLog、Recover、BodyLimit、AcquireTimeout。
// 参数取自 cfg 的对应子段，cfg 不合法时 panic；logger 为 nil 时使用 slog.Default()；
// languages 为 nil 时只使用框架内置的中英文消息，默认语言为 zh-CN。
//
// Timeout 不在其中：它按路由组安装，SSE 等流式路由不能有请求超时。
// RateLimit 等可选中间件同样需要时在其后另行安装；需要不同组合时，直接挑选单个中间件自行组装。
func Defaults(cfg Config, logger *slog.Logger, languages *goi18n.Bundle) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		AssignRequestID(cfg.RequestID),
		NoSniff(),
		ClientIP(cfg.ClientIP),
		Locale(languages),
		AccessLog(cfg.AccessLog, logger),
		Recover(logger),
		BodyLimit(cfg.BodyLimit),
		AcquireTimeout(cfg.AcquireTimeout),
	}
}

// AcquireTimeout 让请求等待共享资源最多 d（acquire.WithTimeout），例如数据库连接池中的空闲连接：
// 连接池用满时，请求在 d 之后以 database.ErrBusy 失败，响应 503 并带 Retry-After，而不是排队到请求超时。
// d 为 0 时什么也不做。
func AcquireTimeout(d time.Duration) func(http.Handler) http.Handler {
	if d < 0 {
		panic("httpx: AcquireTimeout: negative timeout")
	}
	return func(next http.Handler) http.Handler {
		if d == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(acquire.WithTimeout(r.Context(), d)))
		})
	}
}

type requestIDKey struct{}

// RequestID 返回请求标识；未安装 AssignRequestID 时返回空字符串。
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// AssignRequestID 为每个请求确定请求标识，写入 context、日志字段与 X-Request-ID 响应头。
// 入站请求头 cfg.Header 的值合法（1–128 个可打印 ASCII 字符，不含空格）时沿用，否则生成新值。
// 标识同时写入 chi 的 middleware.RequestIDKey，middleware.GetReqID 读到的是同一个值。cfg 不合法时 panic。
func AssignRequestID(cfg RequestIDConfig) func(http.Handler) http.Handler {
	mustValid("AssignRequestID", cfg.Validate())
	header := cfg.Header
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(header)
			if !validRequestID(id) {
				id = rand.Text()
			}
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			ctx = context.WithValue(ctx, middleware.RequestIDKey, id)
			ctx = logging.WithAttrs(ctx, slog.String(logging.AttrRequestID, id))
			w.Header().Set("X-Request-ID", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range []byte(id) {
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}

// NoSniff 设置 X-Content-Type-Options: nosniff。
func NoSniff() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP 把 r.RemoteAddr 改写为客户端地址。
// 只有直连地址属于可信代理时才读取 X-Forwarded-For，并从右向左跳过可信代理；
// 没有可信代理时始终使用直连地址。cfg 不合法时 panic。
func ClientIP(cfg ClientIPConfig) func(http.Handler) http.Handler {
	mustValid("ClientIP", cfg.Validate())
	proxies := append([]netip.Prefix(nil), cfg.TrustedProxies...)
	if len(proxies) == 0 {
		return passthrough
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if addr := clientAddress(r, proxies); addr != r.RemoteAddr {
				r = r.Clone(r.Context())
				r.RemoteAddr = addr
			}
			next.ServeHTTP(w, r)
		})
	}
}

// Recover 恢复处理器 panic 并记录堆栈：尚未写出响应时返回通用 500，否则中止连接。
// logger 为 nil 时使用 slog.Default()。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	logger = componentLogger(logger)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				if recovered == http.ErrAbortHandler {
					panic(recovered)
				}
				logger.ErrorContext(r.Context(), "request panic", "stack", string(debug.Stack()))
				if rw.Status() != 0 {
					panic(http.ErrAbortHandler)
				}
				WriteProblem(rw, http.StatusInternalServerError, "unexpected error occurred")
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// BodyLimit 限制请求体最多 cfg.MaxBytes 字节，声明长度超限时直接返回 413。cfg 不合法时 panic。
func BodyLimit(cfg BodyLimitConfig) func(http.Handler) http.Handler {
	mustValid("BodyLimit", cfg.Validate())
	n := cfg.MaxBytes
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > n {
				WriteProblem(w, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout 为请求 context 设置 cfg.Duration 的超时。它只取消 context，由处理器决定如何响应。
// SSE 开始推送后不受它限制，见 SSE。cfg 不合法时 panic。
func Timeout(cfg TimeoutConfig) func(http.Handler) http.Handler {
	mustValid("Timeout", cfg.Validate())
	d := cfg.Duration
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			ctx = context.WithValue(ctx, untimedKey{}, r.Context())
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// untimedKey 保存 Timeout 设置超时之前的 context，供 SSE 摆脱超时。
type untimedKey struct{}

// withoutTimeout 返回一个保留 ctx 中所有值、但不受 Timeout 超时影响的 context：
// 它仍在客户端断开或服务器关闭时取消。stop 释放相关资源。
func withoutTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	untimed, ok := ctx.Value(untimedKey{}).(context.Context)
	if !ok {
		return context.WithCancel(ctx)
	}
	for {
		outer, ok := untimed.Value(untimedKey{}).(context.Context)
		if !ok {
			break
		}
		untimed = outer
	}
	out, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stop := context.AfterFunc(untimed, cancel)
	return out, func() {
		stop()
		cancel()
	}
}

// Locale 按 Accept-Language 在 languages 中协商语言，把语言与 Localizer 写入 context
// （见 i18n.T），并设置 Content-Language 响应头。languages 为 nil 时只使用框架内置消息，默认语言为 zh-CN。
func Locale(languages *goi18n.Bundle) func(http.Handler) http.Handler {
	if languages == nil {
		var err error
		if languages, err = i18n.New(i18n.LocaleZhCN); err != nil {
			panic(err) // 内置消息总能加载
		}
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			locale := i18n.Match(languages, r.Header.Values("Accept-Language")...)
			w.Header().Set("Content-Language", locale)
			next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), languages, locale)))
		})
	}
}

func passthrough(next http.Handler) http.Handler { return next }

// WriteProblem 在 Huma 之外输出 Problem Details 错误，结构与 huma.NewError 一致。
func WriteProblem(w http.ResponseWriter, status int, detail string) {
	problem := huma.NewError(status, detail)
	ct := "application/json"
	if f, ok := problem.(huma.ContentTypeFilter); ok {
		ct = f.ContentType(ct)
	}
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problem)
}

func clientAddress(r *http.Request, proxies []netip.Prefix) string {
	trusted := func(a netip.Addr) bool {
		for _, p := range proxies {
			if p.Contains(a.Unmap()) {
				return true
			}
		}
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil || !trusted(peer) {
		return r.RemoteAddr
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for j := len(parts) - 1; j >= 0; j-- {
		addr, err := netip.ParseAddr(strings.TrimSpace(parts[j]))
		if err != nil {
			return r.RemoteAddr
		}
		if !trusted(addr) || j == 0 {
			return addr.String()
		}
	}
	return r.RemoteAddr
}
