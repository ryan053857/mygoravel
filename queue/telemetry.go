package queue

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是队列指标和链路的 instrumentation scope。
const scopeName = "github.com/ryan053857/mygoravel/queue"

// 指标和 span 的属性名。驱动和 topic 沿用 OpenTelemetry 消息语义约定的名称，指标和链路按同一个字段筛选。
const (
	attrSystem      = attribute.Key("messaging.system")
	attrDestination = attribute.Key("messaging.destination.name")
	attrState       = attribute.Key("queue.job.state")
	attrOutcome     = attribute.Key("queue.job.outcome")
)

// WithTelemetry 让 Worker 报告 OpenTelemetry 指标和链路。
//
// 指标的属性 messaging.system 是驱动名，messaging.destination.name 是 topic：
//
//   - queue.job.wait（直方图，秒）：任务从可以被领取到开始处理等了多久。驱动给不出可以被领取的时间时不记录，
//     例如 NATS 重新投递的任务。
//   - queue.job.duration（直方图，秒）：处理耗时，属性 queue.job.outcome 是结果：
//     succeeded、retrying、failed、released、lease_lost。按结果计数就是吞吐和失败率。
//
// 每次执行是一个 consumer span，名称是 "process <topic>"，接在发布任务时的链路之后：
// HTTP 请求中发布的任务，执行时出现在同一条链路中。处理失败、重试或租约丢失时 span 标记为错误。
// 不设置时，任务仍然带着发布方的链路：处理函数的 ctx 中是发布方的 span，处理函数自己创建的 span 接在它之后。
//
// 队列中的任务数见 ObserveJobs。mp 或 tp 为 nil 时 panic；只需要其中一种时，另一个传 noop 实现。
func WithTelemetry(mp metric.MeterProvider, tp trace.TracerProvider) WorkerOption {
	if mp == nil || tp == nil {
		panic("queue: nil meter or tracer provider")
	}
	metrics := newJobMetrics(mp)
	tracer := tp.Tracer(scopeName)
	return func(w *Worker) {
		w.tracer = tracer
		w.observers = append(w.observers, metrics.record)
	}
}
