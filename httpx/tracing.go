package httpx

import (
	"net/http"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-chi/chi/v5/middleware"
)

// Tracing 为每个请求创建一个 OpenTelemetry 服务端 span，名称是方法和路由模板，例如 GET /articles/{id}。
// 请求头带有 W3C traceparent 时，span 接在调用方的链路之后。属性遵循 OpenTelemetry 的 HTTP 语义约定，
// 5xx 时 span 标记为错误。处理器和之后的中间件从请求的 context 中拿到这个 span：
// 用它记录的日志带有 trace_id 和 span_id，发布的任务带上链路。
//
// 安装在最外层，在 Metrics 和 Defaults 之前，访问日志才能带上 trace_id：
//
//	router.Use(httpx.Tracing(tp), httpx.Metrics(mp))
//	router.Use(httpx.Defaults(cfg, nil, nil)...)
//
// tp 为 nil 时 panic。
func Tracing(tp trace.TracerProvider) func(http.Handler) http.Handler {
	if tp == nil {
		panic("httpx: nil tracer provider")
	}
	tracer := tp.Tracer(meterName)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			m := method(r.Method)
			ctx, span := tracer.Start(ctx, m, trace.WithSpanKind(trace.SpanKindServer), trace.WithAttributes(
				attribute.String("http.request.method", m),
				attribute.String("url.path", r.URL.Path),
				attribute.String("url.scheme", scheme(r)),
			))
			defer span.End()
			ww, ok := w.(middleware.WrapResponseWriter)
			if !ok {
				ww = middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			}
			next.ServeHTTP(ww, r.WithContext(ctx))

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			span.SetAttributes(attribute.Int("http.response.status_code", status))
			if route := routePattern(r); route != "" {
				span.SetName(m + " " + route)
				span.SetAttributes(attribute.String("http.route", route))
			}
			if status >= 500 {
				span.SetAttributes(attribute.String("error.type", strconv.Itoa(status)))
				span.SetStatus(codes.Error, http.StatusText(status))
			}
		})
	}
}
