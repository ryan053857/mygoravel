package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/logging"
)

// Publisher 把任务写入 queue_jobs 表，实现 message.Publisher。
type Publisher struct {
	db bun.IDB
}

var _ message.Publisher = (*Publisher)(nil)

// NewPublisher 创建发布者。db 可以是 *bun.DB，也可以是 bun.Tx：
// 传入事务时任务随事务一起提交，事务回滚时任务不会被处理。db 必须已执行 Migrations 中的迁移。
func NewPublisher(db bun.IDB) *Publisher {
	if db == nil {
		panic("queue: nil database")
	}
	return &Publisher{db: db}
}

// Publish 实现 message.Publisher：把 msgs 作为 topic 的任务插入，使用第一条消息的 context。
// 消息的 UUID 是任务的主键，必须是合法的 UUID；同一个 UUID 重复发布会返回主键冲突的错误。
func (p *Publisher) Publish(topic string, msgs ...*message.Message) error {
	if topic == "" {
		return errors.New("queue: empty topic")
	}
	if len(msgs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]job, 0, len(msgs))
	for _, msg := range msgs {
		id, err := uuid.Parse(msg.UUID)
		if err != nil {
			return errors.Wrapf(err, "queue: message UUID %q", msg.UUID)
		}
		row := job{ID: id, Topic: topic, Payload: msg.Payload, Metadata: map[string]string{}, AvailableAt: now, CreatedAt: now}
		if row.Payload == nil {
			row.Payload = []byte{}
		}
		for k, v := range msg.Metadata {
			if k != metadataAvailableAt {
				row.Metadata[k] = v
				continue
			}
			at, err := time.Parse(time.RFC3339Nano, v)
			if err != nil {
				return errors.Wrapf(err, "queue: message %s available at", msg.UUID)
			}
			if at.After(now) {
				row.AvailableAt = at.UTC()
			}
		}
		rows = append(rows, row)
	}
	if _, err := p.db.NewInsert().Model(&rows).Exec(msgs[0].Context()); err != nil {
		return errors.Wrapf(err, "queue: publish to %q", topic)
	}
	return nil
}

// Close 实现 message.Publisher，什么也不做：数据库连接由应用管理。
func (p *Publisher) Close() error { return nil }

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

// Publish 把 payload 编码为 JSON，以新的 UUIDv7 作为任务 ID 发布到 topic。
// ctx 中的 request_id 和 trace_id 日志字段会写入任务元数据，处理任务时还原到处理函数的 context 中。
func Publish[T any](ctx context.Context, pub message.Publisher, topic string, payload T, opts ...PublishOption) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return errors.Wrapf(err, "queue: encode %q payload", topic)
	}
	var o publishOptions
	for _, opt := range opts {
		opt(&o)
	}
	msg := message.NewMessage(uuid.Must(uuid.NewV7()).String(), data)
	msg.SetContext(ctx)
	for _, a := range logging.ContextAttrs(ctx) {
		for _, key := range propagated {
			if a.Key == key {
				msg.Metadata.Set(key, a.Value.String())
			}
		}
	}
	if !o.at.IsZero() {
		msg.Metadata.Set(metadataAvailableAt, o.at.UTC().Format(time.RFC3339Nano))
	}
	return pub.Publish(topic, msg)
}
