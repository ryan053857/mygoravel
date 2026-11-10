package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/logging"
)

// PublishOption 调整 Publish 发布的任务。
type PublishOption func(*publishOptions)

type publishOptions struct {
	at time.Time
}

// Delay 让任务在 d 之后才被处理。
func Delay(d time.Duration) PublishOption {
	at := time.Now().Add(d)
	return func(o *publishOptions) { o.at = at }
}

// At 让任务在 t 之后才被处理，t 早于当前时间时立即处理。
func At(t time.Time) PublishOption {
	return func(o *publishOptions) { o.at = t }
}

// propagated 是从发布方 context 带到处理函数 context 的日志字段。
var propagated = []string{logging.AttrRequestID, logging.AttrTraceID}

// Publish 把 payload 编码为 JSON，以新的 UUIDv7 作为任务 ID 交给驱动 d，发布到 topic。
// ctx 中的 request_id 和 trace_id 日志字段会写入任务元数据，处理任务时还原到处理函数的 context 中。
//
//	err := queue.Publish(ctx, queue.Database(tx), "mail.send", msg)
func Publish[T any](ctx context.Context, d Driver, topic string, payload T, opts ...PublishOption) error {
	if topic == "" {
		return errors.New("queue: empty topic")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrapf(err, "queue: encode %q payload", topic)
	}
	var o publishOptions
	for _, opt := range opts {
		opt(&o)
	}
	j := Job{ID: uuid.Must(uuid.NewV7()), Topic: topic, Payload: data, Metadata: map[string]string{}, AvailableAt: o.at}
	for _, a := range logging.ContextAttrs(ctx) {
		for _, key := range propagated {
			if a.Key == key {
				j.Metadata[key] = a.Value.String()
			}
		}
	}
	return errors.Wrapf(d.Enqueue(ctx, j), "queue: publish to %q", topic)
}

// Publisher 把 Watermill 消息作为任务交给驱动，实现 message.Publisher，
// 用于直接发布 *message.Message，例如 Watermill 中间件产生的消息。一般使用 Publish。
type Publisher struct {
	d Driver
}

var _ message.Publisher = (*Publisher)(nil)

// NewPublisher 创建发布到驱动 d 的 Publisher。
func NewPublisher(d Driver) *Publisher {
	if d == nil {
		panic("queue: nil driver")
	}
	return &Publisher{d: d}
}

// Publish 实现 message.Publisher：把 msgs 作为 topic 的任务入队，使用第一条消息的 context。
// 消息的 UUID 是任务 ID，必须是合法的 UUID。元数据 _queue_available_at（RFC 3339）指定最早处理时间。
func (p *Publisher) Publish(topic string, msgs ...*message.Message) error {
	if topic == "" {
		return errors.New("queue: empty topic")
	}
	if len(msgs) == 0 {
		return nil
	}
	jobs := make([]Job, 0, len(msgs))
	for _, msg := range msgs {
		id, err := uuid.Parse(msg.UUID)
		if err != nil {
			return errors.Wrapf(err, "queue: message UUID %q", msg.UUID)
		}
		j := Job{ID: id, Topic: topic, Payload: msg.Payload, Metadata: map[string]string{}}
		for k, v := range msg.Metadata {
			if k != metadataAvailableAt {
				j.Metadata[k] = v
				continue
			}
			if j.AvailableAt, err = time.Parse(time.RFC3339Nano, v); err != nil {
				return errors.Wrapf(err, "queue: message %s available at", msg.UUID)
			}
		}
		jobs = append(jobs, j)
	}
	return errors.Wrapf(p.d.Enqueue(msgs[0].Context(), jobs...), "queue: publish to %q", topic)
}

// Close 实现 message.Publisher，什么也不做：连接由应用管理。
func (p *Publisher) Close() error { return nil }
