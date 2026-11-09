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

type requestIDKey struct{}

// RequestID 返回请求标识，未设置时返回空字符串。
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

type accessLogKey struct{}

// accessLog 收集处理过程中追加到访问日志的字段。
type accessLog struct {
	mu    sync.Mutex
	attrs []any
}

// AddAccessLogAttrs 为当前请求的访问日志追加字段，例如业务错误码；
// 不在默认中间件内或未配置日志器时忽略。
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

// MiddlewareOptions 是 Middleware 的运行时依赖，均可为空。
type MiddlewareOptions struct {
	// Logger 记录访问日志与 panic；nil 表示关闭。
	Logger *slog.Logger
	// Languages 用于 Accept-Language 协商；nil 表示不协商。
	Languages *goi18n.Bundle
}

// Middleware 安装请求关联、访问日志、恢复、限额与语言协商。
// 超时、请求体上限与可信代理取自 cfg；客户端地址沿可信代理链从右向左解析。
func Middleware(cfg Config, opts MiddlewareOptions) func(http.Handler) http.Handler {
	if opts.Logger != nil {
		opts.Logger = slog.New(logging.NewContextHandler(opts.Logger.Handler()))
	}
	cfg = cfg.withDefaults()
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r = r.Clone(r.Context())
			r.RemoteAddr = clientAddress(r, cfg.TrustedProxies)
			id := rand.Text()
			ctx := context.WithValue(r.Context(), requestIDKey{}, id)
			ctx = logging.WithAttrs(ctx, slog.String(logging.AttrRequestID, id))
			access := &accessLog{}
			if opts.Logger != nil {
				ctx = context.WithValue(ctx, accessLogKey{}, access)
			}
			ctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
			defer cancel()

			r = r.WithContext(ctx)
			w.Header().Set("X-Request-ID", id)
			w.Header().Set("X-Content-Type-Options", "nosniff")
			rw := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			defer func() {
				if opts.Logger != nil {
					status := rw.Status()
					if status == 0 {
						status = http.StatusOK
					}
					args := []any{"method", r.Method, "status", status, "duration", time.Since(start), "client", r.RemoteAddr}
					access.mu.Lock()
					args = append(args, access.attrs...)
					access.mu.Unlock()
					opts.Logger.InfoContext(ctx, "http request", args...)
				}
			}()
			defer func() {
				if recovered := recover(); recovered != nil {
					if recovered == http.ErrAbortHandler {
						panic(recovered)
					}
					if opts.Logger != nil {
						opts.Logger.ErrorContext(ctx, "request panic", "stack", string(debug.Stack()))
					}
					if rw.Status() == 0 {
						WriteProblem(rw, http.StatusInternalServerError, "unexpected error occurred")
					} else {
						panic(http.ErrAbortHandler)
					}
				}

			}()
			if r.ContentLength > cfg.MaxBodyBytes {
				WriteProblem(rw, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(rw, r.Body, cfg.MaxBodyBytes)
			if opts.Languages != nil {
				locale := i18n.MatchAcceptLanguage(opts.Languages, r.Header.Get("Accept-Language"))
				ctx = i18n.WithLocale(ctx, locale)
				w.Header().Set("Content-Language", locale)
			}
			r = r.WithContext(ctx)
			next.ServeHTTP(rw, r)
		})
	}
}

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
