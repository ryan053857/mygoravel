package httpx

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是 HTTP 指标和链路的 instrumentation scope。
const scopeName = "github.com/go-tenon/tenon/httpx"

// durationBuckets 是 OpenTelemetry HTTP 语义约定建议的耗时分桶（秒）。
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// Telemetry 为每个请求创建一个 OpenTelemetry 服务端 span，并记录指标 http.server.request.duration（直方图，秒）。
// 属性遵循 OpenTelemetry 的 HTTP 语义约定：http.request.method、http.response.status_code、url.scheme、
// http.route（chi 的路由模板，例如 /articles/{id}），5xx 时还有 error.type，span 标记为错误。
// 没有匹配到路由的请求不带 http.route，不会把任意路径变成指标的属性值；非标准的方法记为 _OTHER。
//
// span 的名称是方法和路由模板，例如 GET /articles/{id}。请求头带有 W3C traceparent 时，span 接在调用方的链路之后。
// 处理器和之后的中间件从请求的 context 中拿到这个 span：用它记录的日志带有 trace_id 和 span_id，
// 发布的任务带上链路，耗时的指标也关联到这条链路。
//
// 安装在 Defaults 之前，访问日志才能带上 trace_id，Recover 产生的 500 也会被记录：
//
//	router.Use(httpx.Telemetry(mp, tp))
//	router.Use(httpx.Defaults(cfg, nil, nil)...)
//
// mp 或 tp 为 nil 时 panic；只需要其中一种时，另一个传 noop 实现。
func Telemetry(mp metric.MeterProvider, tp trace.TracerProvider) func(http.Handler) http.Handler {
	if mp == nil || tp == nil {
		panic("httpx: nil meter or tracer provider")
	}
	duration, err := mp.Meter(scopeName).Float64Histogram("http.server.request.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP server requests."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "httpx: create http.server.request.duration"))
	}
	tracer := tp.Tracer(scopeName)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			m := method(r.Method)
			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", m),
				attribute.String("url.scheme", scheme(r)),
			}
			ctx := propagation.TraceContext{}.Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, m, trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(append(attrs, attribute.String("url.path", r.URL.Path))...))
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
			result := []attribute.KeyValue{attribute.Int("http.response.status_code", status)}
			if route := routePattern(r); route != "" {
				span.SetName(m + " " + route)
				result = append(result, attribute.String("http.route", route))
			}
			if status >= 500 {
				result = append(result, attribute.String("error.type", strconv.Itoa(status)))
				span.SetStatus(codes.Error, http.StatusText(status))
			}
			span.SetAttributes(result...)
			duration.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(append(attrs, result...)...))
		})
	}
}

// method 返回标准方法名；其他方法记为 _OTHER，避免客户端随意构造的方法名成为属性值。
func method(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodConnect, http.MethodOptions, http.MethodTrace:
		return m
	}
	return "_OTHER"
}

func scheme(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

// routePattern 返回 chi 匹配到的路由模板，没有匹配或不在 chi 路由中时返回空字符串。
func routePattern(r *http.Request) string {
	if rctx := chi.RouteContext(r.Context()); rctx != nil {
		return rctx.RoutePattern()
	}
	return ""
}
