package queue

import (
	"context"

	"github.com/ThreeDotsLabs/watermill/message"
)

// Topic 是带有 payload 类型的 topic 名称。把它声明为包级变量，发布和处理共用同一个声明，
// topic 名称和 payload 类型就不会在两处写得不一致：
//
//	var PublishJob = queue.Topic[uuid.UUID]("articles.publish")
//
//	err := PublishJob.Publish(ctx, queue.Database(tx), item.ID, queue.At(item.PublishAt))
//	PublishJob.Handle(router, sub, publish.Job)
type Topic[T any] string

// Publish 等同于 queue.Publish(ctx, d, string(t), payload, opts...)。
func (t Topic[T]) Publish(ctx context.Context, d Driver, payload T, opts ...PublishOption) error {
	return Publish(ctx, d, string(t), payload, opts...)
}

// Handle 等同于 queue.Handle(r, sub, string(t), fn)。
func (t Topic[T]) Handle(r *message.Router, sub message.Subscriber, fn func(ctx context.Context, payload T) error) *message.Handler {
	return Handle(r, sub, string(t), fn)
}
