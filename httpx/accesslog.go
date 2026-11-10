package httpx

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	slogchi "github.com/samber/slog-chi"

	"github.com/ryan053857/mygoravel/logging"
)

// AccessLog 在请求结束后用 slog-chi 记录一条消息为 "http request" 的访问日志：
// request 分组含方法、路径、路由模板、客户端地址等，response 分组含状态码、耗时、长度，
// 以及 AddAccessLogAttrs 追加的字段。5xx 记为 Error，4xx 记为 Warn，其余为 Info。
//
// 安装在 Recover 之外，才能记录 panic 产生的 500；request_id 由 AssignRequestID 写入的
// context 字段提供。cfg 不合法时 panic；logger 为 nil 时使用 slog.Default()。
func AccessLog(cfg AccessLogConfig, logger *slog.Logger) func(http.Handler) http.Handler {
	mustValid("AccessLog", cfg.Validate())
	logged := slogchi.NewWithConfig(componentLogger(logger), slogchi.Config{
		DefaultLevel:       slog.LevelInfo,
		ClientErrorLevel:   slog.LevelWarn,
		ServerErrorLevel:   slog.LevelError,
		WithUserAgent:      cfg.UserAgent,
		WithRequestHeader:  cfg.RequestHeader,
		WithResponseHeader: cfg.ResponseHeader,
		WithRequestBody:    cfg.RequestBody,
		WithResponseBody:   cfg.ResponseBody,
		WithTraceID:        cfg.TraceID,
		WithSpanID:         cfg.TraceID,
		WithClientIP:       true,
		// request_id 已由 context 字段输出，不再重复记录 slog-chi 的 id 字段。
		WithRequestID:     false,
		WithCustomMessage: func(http.ResponseWriter, *http.Request) string { return "http request" },
		Filters:           []slogchi.Filter{slogchi.IgnorePath(cfg.IgnorePaths...)},
	})
	return func(next http.Handler) http.Handler {
		h := logged(implicitOK(next))
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// slog-chi 读取 chi 的路由上下文；在 chi 路由之外使用时补一个空的。
			if chi.RouteContext(r.Context()) == nil {
				r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
			}
			h.ServeHTTP(w, r)
		})
	}
}

// componentLogger 解析可选的 logger，并标记 component=http，可用 logs.levels.http 单独调整级别。
func componentLogger(logger *slog.Logger) *slog.Logger {
	return logging.Or(logger).With(logging.AttrComponent, "http")
}

// implicitOK 在处理器未写出任何响应时显式写出 200，使访问日志记录的状态码与客户端收到的一致。
// 升级连接（例如 WebSocket）不处理。
func implicitOK(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r)
		if ww, ok := w.(middleware.WrapResponseWriter); ok && ww.Status() == 0 && r.Header.Get("Upgrade") == "" {
			w.WriteHeader(http.StatusOK)
		}
	})
}

// AddAccessLogAttrs 为当前请求的访问日志追加顶层字段，例如业务错误码；同名字段后写覆盖先写。
// 未安装 AccessLog 时忽略。
func AddAccessLogAttrs(ctx context.Context, attrs ...slog.Attr) {
	slogchi.AddContextAttributes(ctx, attrs...)
}
