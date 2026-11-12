package logging_test

import (
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/logging"
)

var errSlugTaken = errorx.Define(errorx.Internal, "article.slug_taken")

type pgError struct{}

func (pgError) Error() string { return "duplicate key" }

func TestRecordError(t *testing.T) {
	t.Parallel()
	spans := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)).Tracer("test")
	record := func(err error) map[attribute.Key]string {
		t.Helper()
		_, span := tracer.Start(context.Background(), "op")
		logging.RecordError(span, err)
		span.End()
		ended := spans.Ended()
		events := ended[len(ended)-1].Events()
		if err == nil {
			if len(events) != 0 {
				t.Fatalf("nil error recorded %+v", events)
			}
			return nil
		}
		if len(events) != 1 || events[0].Name != "exception" {
			t.Fatalf("events %+v", events)
		}
		attrs := map[attribute.Key]string{}
		for _, a := range events[0].Attributes {
			attrs[a.Key] = a.Value.AsString()
		}
		return attrs
	}

	// 错误码作为类型，堆栈是错误创建处的。
	got := record(errors.Wrap(errSlugTaken.New(), "update article"))
	if got["exception.type"] != "article.slug_taken" || got["exception.message"] != "update article: article.slug_taken" ||
		!strings.Contains(got["exception.stacktrace"], "TestRecordError") {
		t.Errorf("coded error %v", got)
	}

	// 没有错误码时是最内层错误的类型。
	got = record(errors.Wrap(pgError{}, "insert"))
	if got["exception.type"] != "logging_test.pgError" || got["exception.message"] != "insert: duplicate key" {
		t.Errorf("wrapped error %v", got)
	}

	// 普通错误没有堆栈，不重复 message。
	got = record(fmt.Errorf("plain: %w", stderrors.New("x")))
	if _, ok := got["exception.stacktrace"]; ok || got["exception.type"] != "*errors.errorString" {
		t.Errorf("plain error %v", got)
	}

	record(nil)

	// 不在记录的 span 什么也不做。
	logging.RecordError(trace.SpanFromContext(context.Background()), errors.New("x"))
}
