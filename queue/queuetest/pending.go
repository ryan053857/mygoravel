package queuetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/queue"
)

// Pending 返回 db 的 queue_outbox 中 topic 还没转发的任务的 payload，按发布顺序。
// 用于只检查发布了哪些任务、不运行 Worker 的测试；Worker 运行时任务会被转发并删除。读取或解码失败时使测试失败。
//
//	jobs := queuetest.Pending(t, db, article.PublishJob)
func Pending[T any](t testing.TB, db bun.IDB, topic queue.Topic[T]) []T {
	t.Helper()
	var bodies [][]byte
	err := db.NewSelect().Table("queue_outbox").Column("body").
		Where("topic = ?", string(topic)).OrderExpr("id").Scan(context.Background(), &bodies)
	if err != nil {
		t.Fatalf("queuetest: read queue_outbox: %v", err)
	}
	payloads := make([]T, len(bodies))
	for i, body := range bodies {
		var env struct {
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			t.Fatalf("queuetest: decode %q job: %v", topic, err)
		}
		if err := json.Unmarshal(env.Payload, &payloads[i]); err != nil {
			t.Fatalf("queuetest: decode %q payload: %v", topic, err)
		}
	}
	return payloads
}
