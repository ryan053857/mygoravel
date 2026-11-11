package httpx

import (
	"net/http"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// meterName 是 HTTP 指标的 instrumentation scope。
const meterName = "github.com/ryan053857/mygoravel/httpx"

// durationBuckets 是 OpenTelemetry HTTP 语义约定建议的耗时分桶（秒）。
var durationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.25, 0.5, 0.75, 1, 2.5, 5, 7.5, 10}

// Metrics 把每个请求记录为 OpenTelemetry 指标 http.server.request.duration（直方图，秒），
// 属性遵循 OpenTelemetry 的 HTTP 语义约定：http.request.method、http.response.status_code、url.scheme、
// http.route（chi 的路由模板，例如 /articles/{id}），5xx 时还有 error.type。
// 没有匹配到路由的请求不带 http.route，不会把任意路径变成属性值。
//
// 安装在 Defaults 之前，才能记录 Recover 产生的 500：
//
//	router.Use(httpx.Metrics(mp))
//	router.Use(httpx.Defaults(cfg, nil, nil)...)
//
// mp 为 nil 时 panic。
func Metrics(mp metric.MeterProvider) func(http.Handler) http.Handler {
	if mp == nil {
		panic("httpx: nil meter provider")
	}
	duration, err := mp.Meter(meterName).Float64Histogram("http.server.request.duration", metric.WithUnit("s"),
		metric.WithDescription("Duration of HTTP server requests."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "httpx: create http.server.request.duration"))
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww, ok := w.(middleware.WrapResponseWriter)
			if !ok {
				ww = middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			}
			next.ServeHTTP(ww, r)

			status := ww.Status()
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []attribute.KeyValue{
				attribute.String("http.request.method", method(r.Method)),
				attribute.Int("http.response.status_code", status),
				attribute.String("url.scheme", scheme(r)),
			}
			if route := routePattern(r); route != "" {
				attrs = append(attrs, attribute.String("http.route", route))
			}
			if status >= 500 {
				attrs = append(attrs, attribute.String("error.type", strconv.Itoa(status)))
			}
			duration.Record(r.Context(), time.Since(start).Seconds(), metric.WithAttributes(attrs...))
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
