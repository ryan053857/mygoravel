package queue

import (
	"context"
	"encoding/json"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Topic 是带有 payload 类型的 topic 名称，发布和处理任务都通过它。把它声明为包级变量，
// 发布和处理共用同一个声明，topic 名称和 payload 类型就不会在两处写得不一致：
//
//	var PublishAt = queue.Topic[uuid.UUID]("articles.publish")
//
//	err := PublishAt.Publish(ctx, tx, item.ID, queue.At(item.PublishAt))
//	PublishAt.Handle(w, publish.Job)
//
// 名称是 NSQ 的 topic，由 1 到 64 个字母、数字、'.'、'_' 或 '-' 组成，建议以应用名开头，例如 blog.mail.send。
type Topic[T any] string

// Publish 发布一个任务：把 payload 编码为 JSON，写入 db 的 queue_outbox，由 Worker 转发到 NSQ。
// db 通常是修改业务数据的事务：事务提交后任务才会被转发，回滚时一起取消。
// ctx 中的 request_id 日志字段和 OpenTelemetry 链路（W3C traceparent）随任务带到处理函数的 context 中。
func (t Topic[T]) Publish(ctx context.Context, db bun.IDB, payload T, opts ...PublishOption) error {
	return publish(ctx, db, string(t), []T{payload}, opts...)
}

// PublishAll 与 Publish 相同，每个 payload 发布为一个任务，批量写入。payloads 为空时什么也不做。
func (t Topic[T]) PublishAll(ctx context.Context, db bun.IDB, payloads []T, opts ...PublishOption) error {
	return publish(ctx, db, string(t), payloads, opts...)
}

// Handle 把 fn 注册为这个 topic 在 w 上的处理函数。payload 按 JSON 解码为 T，解码失败视为永久失败。
// fn 返回 nil 时任务完成；返回错误时按退避时间重试，返回 RetryAfter 包装的错误时在指定的时间之后重试，
// 返回 Permanent 包装的错误或次数用完时转入失败任务表。fn 中的 panic 视为返回错误。
//
// 同一个 topic 只能注册一次。topic 不合法、重复注册或者在 w.Run 之后调用时 panic。
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
