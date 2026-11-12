package rest_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// TestServerErrorRecordedOnSpan 检查 5xx 的错误链记录在请求的 span 上，4xx 的业务错误不记录。
func TestServerErrorRecordedOnSpan(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewSpanRecorder()
	router := chi.NewRouter()
	router.Use(httpx.Telemetry(metricnoop.NewMeterProvider(), sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))))
	logger := slog.New(slog.DiscardHandler)
	router.Use(httpx.Defaults(httpx.DefaultConfig(), logger, nil)...)
	api := rest.New(rest.Options{Router: router, Accounts: identity.Accounts{"user": nil}, Logger: logger})
	for path, err := range map[string]error{
		"/crash": errors.Wrap(errors.New("connection reset"), "load article"),
		"/fail":  errorx.Define(errorx.NotFound, "article.not_found").New(),
	} {
		op := huma.Operation{OperationID: path[1:], Method: "GET", Path: path}
		rest.Public()(&op)
		rest.Register(api, op, func(context.Context, *pingIn) (*pingOut, error) { return nil, err })
	}
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/crash", nil))
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/fail", nil))

	ended := spans.Ended()
	if len(ended) != 2 {
		t.Fatalf("%d spans", len(ended))
	}
	crash, fail := ended[0], ended[1]
	if ev := crash.Events(); len(ev) != 1 || ev[0].Name != "exception" {
		t.Fatalf("crash events %+v", ev)
	}
	var message, stack string
	for _, a := range crash.Events()[0].Attributes {
		switch a.Key {
		case "exception.message":
			message = a.Value.AsString()
		case "exception.stacktrace":
			stack = a.Value.AsString()
		}
	}
	if message != "load article: connection reset" || !strings.Contains(stack, "TestServerErrorRecordedOnSpan") {
		t.Fatalf("message %q stack %q", message, stack)
	}
	if len(fail.Events()) != 0 {
		t.Fatalf("4xx events %+v", fail.Events())
	}
}
