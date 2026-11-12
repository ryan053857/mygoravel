package logging

import (
	"fmt"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/errorx"
)

// RecordError 把 err 记录为 span 上的 exception 事件，字段与 ErrorFormatter 的对应：
//
//   - exception.message 是 err.Error()；
//   - exception.type 是错误码，没有时是最内层错误的类型，例如 *pgconn.PgError；
//   - exception.stacktrace 是 %+v 输出的完整错误链，包含错误创建处的堆栈；与 message 相同时省略。
//
// 链路平台据此按类型聚合错误、展示堆栈。它不修改 span 的状态，调用方按自己的语义设置。
// err 为 nil 或 span 不在记录时什么也不做。
//
// span.RecordError 记录的类型是最外层包装的类型（例如 *withstack.withStack），堆栈是调用它的位置，
// 因此框架的组件都使用本函数。
func RecordError(span trace.Span, err error) {
	if err == nil || !span.IsRecording() {
		return
	}
	msg := err.Error()
	typ := errorx.CodeOf(err)
	if typ == "" {
		typ = fmt.Sprintf("%T", errors.UnwrapAll(err))
	}
	attrs := []attribute.KeyValue{semconv.ExceptionType(typ), semconv.ExceptionMessage(msg)}
	if stack := fmt.Sprintf("%+v", err); stack != msg {
		attrs = append(attrs, semconv.ExceptionStacktrace(stack))
	}
	span.AddEvent(semconv.ExceptionEventName, trace.WithAttributes(attrs...))
}
