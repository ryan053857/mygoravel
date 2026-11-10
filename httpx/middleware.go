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
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5/middleware"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/logging"
)

// Defaults 返回每个应用都应安装的基础中间件，按执行顺序排列：
// AssignRequestID、NoSniff、ClientIP、AccessLog、Recover、BodyLimit、Timeout。
// 参数取自 cfg 的对应子段；cfg 不合法或 logger 为 nil 时 panic。
//
// RateLimit、Locale 等可选中间件需要时在其后另行安装；
// 需要不同组合时（例如流式接口不设超时），直接挑选单个中间件自行组装。
func Defaults(cfg Config, logger *slog.Logger) []func(http.Handler) http.Handler {
	return []func(http.Handler) http.Handler{
		AssignRequestID(),
		NoSniff(),
		ClientIP(cfg.ClientIP),
		AccessLog(logger),
		Recover(logger),
		BodyLimit(cfg.BodyLimit),
		Timeout(cfg.Timeout),
	}
}

type requestIDKey struct{}

// RequestID 返回请求标识；未安装 AssignRequestID 时返回空字符串。
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// AssignRequestID 为每个请求确定请求标识，写入 context、日志字段与 X-Request-ID 响应头。
// 入站 X-Request-ID 合法（1–128 个字母、数字或 -_.:）时沿用，否则生成新值。
func AssignRequestID() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if !validRequestID(id) {
				id = rand.Text()
			}
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
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
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
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

type accessLogKey struct{}

// accessLog 收集处理过程中追加到访问日志的字段。
type accessLog struct {
	mu    sync.Mutex
	attrs []any
}

// AddAccessLogAttrs 为当前请求的访问日志追加字段，例如业务错误码；
// 未安装 AccessLog 时忽略。
func AddAccessLogAttrs(ctx context.Context, attrs ...slog.Attr) {
	l, ok := ctx.Value(accessLogKey{}).(*accessLog)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range attrs {
		l.attrs = append(l.attrs, a)
	}
}

// AccessLog 在请求结束后记录方法、状态码、耗时、客户端地址与 AddAccessLogAttrs 追加的字段。
// 安装在 Recover 之外，才能记录 panic 产生的 500。logger 为 nil 时 panic。
func AccessLog(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		panic("httpx: AccessLog: nil logger")
	}
	logger = slog.New(logging.NewContextHandler(logger.Handler()))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			access := &accessLog{}
			ctx := context.WithValue(r.Context(), accessLogKey{}, access)
			rw := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				status := rw.Status()
				if status == 0 {
					status = http.StatusOK
				}
				args := []any{"method", r.Method, "status", status, "duration", time.Since(start), "client", r.RemoteAddr}
				access.mu.Lock()
				args = append(args, access.attrs...)
				access.mu.Unlock()
				logger.InfoContext(ctx, "http request", args...)
			}()
			next.ServeHTTP(rw, r.WithContext(ctx))
		})
	}
}

// Recover 恢复处理器 panic 并记录堆栈：尚未写出响应时返回通用 500，否则中止连接。
// logger 为 nil 时 panic。
func Recover(logger *slog.Logger) func(http.Handler) http.Handler {
	if logger == nil {
		panic("httpx: Recover: nil logger")
	}
	logger = slog.New(logging.NewContextHandler(logger.Handler()))
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

// Timeout 为请求 context 设置 cfg.Duration 的超时。
// 它只取消 context，由处理器决定如何响应。cfg 不合法时 panic。
func Timeout(cfg TimeoutConfig) func(http.Handler) http.Handler {
	mustValid("Timeout", cfg.Validate())
	d := cfg.Duration
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// Locale 按 Accept-Language 在 languages 中协商语言，写入 context 与 Content-Language 响应头。
// languages 为 nil 时 panic。
func Locale(languages *goi18n.Bundle) func(http.Handler) http.Handler {
	if languages == nil {
		panic("httpx: Locale: nil languages")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			locale := i18n.MatchAcceptLanguage(languages, r.Header.Get("Accept-Language"))
			w.Header().Set("Content-Language", locale)
			next.ServeHTTP(w, r.WithContext(i18n.WithLocale(r.Context(), locale)))
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
