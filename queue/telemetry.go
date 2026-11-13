package queue

import (
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是队列指标和链路的 instrumentation scope。
const scopeName = "github.com/go-tenon/tenon/queue"

// 指标和 span 的属性名。消息系统和 topic 沿用 OpenTelemetry 消息语义约定的名称，指标和链路按同一个字段筛选。
const (
	attrSystem      = attribute.Key("messaging.system")
	attrDestination = attribute.Key("messaging.destination.name")
	attrOutcome     = attribute.Key("queue.job.outcome")
	attrRoute       = attribute.Key("queue.relay.route")
)

// system 是 messaging.system 属性的值。
const system = "nsq"

// WithTelemetry 让 Worker 报告 OpenTelemetry 指标和链路。
//
// 指标的属性 messaging.system 是 nsq，messaging.destination.name 是 topic：
//
//   - queue.job.wait（直方图，秒）：任务从可以执行到开始处理等了多久。NSQ 重新投递的任务给不出可以执行的时间，不记录。
//   - queue.job.duration（直方图，秒）：处理耗时，属性 queue.job.outcome 是结果：
//     succeeded、retrying、failed、released。按结果计数就是吞吐和失败率。
//
// 转发的指标没有 messaging 属性：
//
//   - queue.relay.jobs（计数）：从 WAL 读到并转发的任务数，属性 queue.relay.route 是去向：
//     published（立即交给 NSQ）、deferred（NSQ 延迟消息）、scheduled（存入 queue_scheduled）。
//   - queue.relay.active（仪表）：这个实例正在转发时为 1，否则为 0。所有实例之和为 0 时没有实例在转发，WAL 会一直累积。
//   - queue.relay.lag（仪表，字节）：复制槽中还没确认的 WAL，每个实例采集时查询一次。
//   - queue.scheduled.jobs（仪表）：queue_scheduled 中等待的任务数。
//
// 每次执行是一个 consumer span，名称是 "process <topic>"，接在发布任务时的链路之后：
// HTTP 请求中发布的任务，执行时出现在同一条链路中。处理失败或重试时 span 标记为错误。
// 不设置时，任务仍然带着发布方的链路：处理函数的 ctx 中是发布方的 span，处理函数自己创建的 span 接在它之后。
//
// NSQ 中积压的任务数由 nsqd 统计，在 nsqadmin 中查看。mp 或 tp 为 nil 时 panic；只需要其中一种时，另一个传 noop 实现。
func WithTelemetry(mp metric.MeterProvider, tp trace.TracerProvider) WorkerOption {
	if mp == nil || tp == nil {
		panic("queue: nil meter or tracer provider")
	}
	metrics := newJobMetrics(mp)
	tracer := tp.Tracer(scopeName)
	return func(w *Worker) {
		w.tracer = tracer
		w.observers = append(w.observers, metrics.record)
		observeRelay(mp, w)
	}
}
