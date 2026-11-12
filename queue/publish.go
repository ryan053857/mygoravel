package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel/propagation"

	"github.com/ryan053857/mygoravel/logging"
)

// notifyChannel 是发布任务时通知 Worker 的 PostgreSQL 频道。
const notifyChannel = "queue_outbox"

// insertBatch 是一条 INSERT 写入 queue_outbox 的最多行数。
const insertBatch = 1000

// PublishOption 调整 Topic.Publish 发布的任务。
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
var propagated = []string{logging.AttrRequestID}

// publish 把 payloads 写入 queue_outbox，每个 payload 一个任务，以新的 UUIDv7 作为任务 ID。
// ctx 中的 request_id 日志字段和 OpenTelemetry 链路（W3C traceparent）写入任务元数据，处理时还原到处理函数的 context 中。
// db 是事务时，任务随事务提交；提交时通知 Worker 立即转发。
func publish[T any](ctx context.Context, db bun.IDB, topic string, payloads []T, opts ...PublishOption) error {
	if db == nil {
		panic("queue: nil database")
	}
	if !validName(topic) {
		return errors.Newf("queue: topic %q must be 1 to 64 letters, digits, '.', '_' or '-'", topic)
	}
	if len(payloads) == 0 {
		return nil
	}
	var o publishOptions
	for _, opt := range opts {
		opt(&o)
	}
	now := time.Now().UTC()
	at := now
	if o.at.After(now) {
		at = o.at.UTC()
	}
	metadata := map[string]string{}
	for _, a := range logging.ContextAttrs(ctx) {
		for _, key := range propagated {
			if a.Key == key {
				metadata[key] = a.Value.String()
			}
		}
	}
	traceContext.Inject(ctx, propagation.MapCarrier(metadata))

	rows := make([]outboxRow, len(payloads))
	for i, p := range payloads {
		data, err := json.Marshal(p)
		if err != nil {
			return errors.Wrapf(err, "queue: encode %q payload", topic)
		}
		body, err := json.Marshal(envelope{ID: uuid.Must(uuid.NewV7()), Payload: data, Metadata: metadata, AvailableAt: at})
		if err != nil {
			return errors.Wrapf(err, "queue: encode %q job", topic)
		}
		rows[i] = outboxRow{Topic: topic, Body: body, AvailableAt: at, CreatedAt: now}
	}
	return errors.Wrapf(insertOutbox(ctx, db, rows), "queue: publish to %q", topic)
}

// insertOutbox 写入 rows 并通知 Worker。在事务中时通知随提交送达。
func insertOutbox(ctx context.Context, db bun.IDB, rows []outboxRow) error {
	for start := 0; start < len(rows); start += insertBatch {
		batch := rows[start:min(start+insertBatch, len(rows))]
		if _, err := db.NewInsert().Model(&batch).Exec(ctx); err != nil {
			return err
		}
	}
	_, err := db.NewRaw("SELECT pg_notify(?, '')", notifyChannel).Exec(ctx)
	return err
}
