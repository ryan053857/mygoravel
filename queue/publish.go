package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"go.opentelemetry.io/otel/propagation"

	"github.com/ryan053857/mygoravel/logging"
)

// emitBatch 是一条语句写入 WAL 的最多任务数。
const emitBatch = 1000

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

// publish 把 payloads 作为任务写入 db 所在数据库的 WAL，每个 payload 一个任务，以新的 UUIDv7 作为任务 ID。
// ctx 中的 request_id 日志字段和 OpenTelemetry 链路（W3C traceparent）写入任务元数据，处理时还原到处理函数的 context 中。
// db 是事务时，任务随事务提交，回滚时一起取消；Worker 经由逻辑复制在提交后立即收到。
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

	bodies := make([][]byte, len(payloads))
	for i, p := range payloads {
		data, err := json.Marshal(p)
		if err != nil {
			return errors.Wrapf(err, "queue: encode %q payload", topic)
		}
		body, err := json.Marshal(envelope{ID: uuid.Must(uuid.NewV7()), Payload: data, Metadata: metadata, AvailableAt: at})
		if err != nil {
			return errors.Wrapf(err, "queue: encode %q job", topic)
		}
		bodies[i] = body
	}
	return errors.Wrapf(emit(ctx, db, topic, bodies), "queue: publish to %q", topic)
}

// emit 把发给 NSQ 的消息 bodies 写入 WAL：事务性的逻辑解码消息只有在事务提交后才会被解码，回滚时一起丢弃。
// 它们不写入任何表，只是一条 WAL 记录，所以发布任务的事务之间不会互相等待。
func emit(ctx context.Context, db bun.IDB, topic string, bodies [][]byte) error {
	for start := 0; start < len(bodies); start += emitBatch {
		batch := bodies[start:min(start+emitBatch, len(bodies))]
		_, err := db.NewRaw("SELECT pg_logical_emit_message(true, ?, b) FROM unnest(?::bytea[]) AS b",
			MessagePrefix+topic, pgdialect.Array(batch)).Exec(ctx)
		if err != nil {
			return err
		}
	}
	return nil
}
