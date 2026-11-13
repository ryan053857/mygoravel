package queuetest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pglogrepl"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/queue"
)

// Pending 返回 db 中 topic 还没转发的任务的 payload：先是 WAL 中的，按提交顺序，再是 queue_scheduled 中的，按存入顺序。
// 用于只检查发布了哪些任务、不运行 Worker 的测试；Worker 运行时任务会被转发，复制槽也被它占用，不能同时读取。
// 只能看到已经提交的任务。读取或解码失败时使测试失败。
//
//	jobs := queuetest.Pending(t, db, article.PublishJob)
func Pending[T any](t testing.TB, db bun.IDB, topic queue.Topic[T]) []T {
	t.Helper()
	ctx := context.Background()
	var database string
	if err := db.NewRaw("SELECT current_database()").Scan(ctx, &database); err != nil {
		t.Fatalf("queuetest: current database: %v", err)
	}
	// peek 只读取，不推进复制槽的位置。
	var changes [][]byte
	err := db.NewRaw(`SELECT data FROM pg_logical_slot_peek_binary_changes(?, NULL, NULL,
		'proto_version', '1', 'publication_names', ?, 'messages', 'true')`,
		queue.SlotName(database), queue.Publication).Scan(ctx, &changes)
	if err != nil {
		t.Fatalf("queuetest: read published jobs: %v", err)
	}
	var bodies [][]byte
	for _, data := range changes {
		msg, err := pglogrepl.Parse(data)
		if err != nil {
			t.Fatalf("queuetest: decode WAL data: %v", err)
		}
		if m, ok := msg.(*pglogrepl.LogicalDecodingMessage); ok && m.Prefix == queue.MessagePrefix+string(topic) {
			bodies = append(bodies, m.Content)
		}
	}
	var scheduled [][]byte
	err = db.NewSelect().Table("queue_scheduled").Column("body").
		Where("topic = ?", string(topic)).OrderExpr("created_at, id").Scan(ctx, &scheduled)
	if err != nil {
		t.Fatalf("queuetest: read queue_scheduled: %v", err)
	}
	bodies = append(bodies, scheduled...)

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
