package database

import (
	"context"

	"go.opentelemetry.io/otel/trace"
)

// ChildSpans 包装 tp，只在 ctx 中已有 span 时创建 span，用于数据库查询的链路追踪：
//
//	db.AddQueryHook(bunotel.NewQueryHook(bunotel.WithTracerProvider(database.ChildSpans(tp))))
//
// bunotel 为每条查询都创建 span，不管 ctx 中有没有上级 span。队列轮询、调度器加锁这些后台查询不属于任何请求，
// 直接传入 tp 时，它们每秒都会各自成为一条只有一个 span 的链路。经过 ChildSpans，查询只出现在
// 请求、任务等已有的链路中；没有上级 span 时返回 ctx 中不记录的空 span，没有开销。
//
// tp 为 nil 时 panic。
func ChildSpans(tp trace.TracerProvider) trace.TracerProvider {
	if tp == nil {
		panic("database: nil tracer provider")
	}
	return childSpans{tp}
}

type childSpans struct{ trace.TracerProvider }

func (p childSpans) Tracer(name string, opts ...trace.TracerOption) trace.Tracer {
	return childTracer{p.TracerProvider.Tracer(name, opts...)}
}

type childTracer struct{ trace.Tracer }

func (t childTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	if !trace.SpanContextFromContext(ctx).IsValid() {
		return ctx, trace.SpanFromContext(ctx)
	}
	return t.Tracer.Start(ctx, name, opts...)
}
