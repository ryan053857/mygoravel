package queue

import (
	"context"
	"encoding/json"

	"github.com/cockroachdb/errors"
)

// Topic 是带有 payload 类型的 topic 名称，发布和处理任务都通过它。把它声明为包级变量，
// 发布和处理共用同一个声明，topic 名称和 payload 类型就不会在两处写得不一致：
//
//	var PublishAt = queue.Topic[uuid.UUID]("articles.publish")
//
//	err := PublishAt.Publish(ctx, queue.Database(tx), item.ID, queue.At(item.PublishAt))
//	PublishAt.Handle(w, publish.Job)
type Topic[T any] string

// Publish 把 payload 编码为 JSON，以新的 UUIDv7 作为任务 ID 交给驱动 d。
// ctx 中的 request_id 日志字段和 OpenTelemetry 链路（W3C traceparent）写入任务元数据，
// 处理任务时还原到处理函数的 context 中。d 是传入 bun.Tx 的 Database 时，任务随事务一起提交。
func (t Topic[T]) Publish(ctx context.Context, d Driver, payload T, opts ...PublishOption) error {
	return publish(ctx, d, string(t), payload, opts...)
}

// Handle 把 fn 注册为这个 topic 在 w 上的处理函数。payload 按 JSON 解码为 T，解码失败视为永久失败。
// fn 返回 nil 时任务完成；返回错误时按退避时间重试，返回 RetryAfter 包装的错误时在指定的时间之后重试，
// 返回 Permanent 包装的错误或次数用完时转入失败任务表。fn 中的 panic 视为返回错误。
//
// 同一个 topic 只能注册一次。topic 为空、重复注册或者在 w.Run 之后调用时 panic。
func (t Topic[T]) Handle(w *Worker, fn func(ctx context.Context, payload T) error, opts ...HandleOption) {
	topic := string(t)
	w.handle(topic, func(ctx context.Context, data []byte) error {
		var payload T
		if err := json.Unmarshal(data, &payload); err != nil {
			return Permanent(errors.Wrapf(err, "queue: decode %q payload", topic))
		}
		return fn(ctx, payload)
	}, opts)
}
