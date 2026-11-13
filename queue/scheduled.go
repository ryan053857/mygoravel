package queue

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// scheduleBatch 是调度一次移交的最多任务数。
const scheduleBatch = 500

// schedule 把任务写入 queue_scheduled，到时间再由 dispatch 交回 WAL。同一个任务已经在表中时什么也不做：
// relay 在确认位置之前崩溃时会再次收到同一批任务。
func schedule(ctx context.Context, db bun.IDB, rows []scheduledRow) error {
	if len(rows) == 0 {
		return nil
	}
	_, err := db.NewInsert().Model(&rows).On("CONFLICT (id) DO NOTHING").Exec(ctx)
	return errors.Wrap(err, "queue: schedule jobs")
}

// scheduleInterval 返回检查 queue_scheduled 的间隔：任务在离执行不超过 MaxDefer/2 时移交，
// 每 MaxDefer/4 检查一次，所以不会错过；最长 30 秒。
func (w *Worker) scheduleInterval() time.Duration {
	return min(w.cfg.MaxDefer/4, 30*time.Second)
}

// dispatchLoop 定期把快到时间的任务交回 WAL，直到 ctx 结束。所有 Worker 都运行它，SKIP LOCKED 让它们互不重复。
func (w *Worker) dispatchLoop(ctx context.Context) {
	for {
		n, err := w.dispatch(ctx)
		if err != nil && ctx.Err() == nil {
			w.logger.Error("dispatch scheduled jobs failed", "error", err)
		}
		if err == nil && n == scheduleBatch {
			continue // 可能还有，接着移交
		}
		wait(ctx, w.scheduleInterval())
		if ctx.Err() != nil {
			return
		}
	}
}

// dispatch 在一个事务中锁住一批离执行不超过 MaxDefer/2 的任务，把它们写入 WAL 后删除，返回移交的条数。
// 写入 WAL 与删除在同一个事务中，任务不会丢失也不会重复；relay 收到后按剩余时间交给 NSQ 延迟投递。
func (w *Worker) dispatch(ctx context.Context) (int, error) {
	var n int
	err := w.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var rows []scheduledRow
		err := tx.NewSelect().Model(&rows).Where("s.available_at <= ?", time.Now().Add(w.cfg.MaxDefer/2).UTC()).
			OrderExpr("s.available_at").Limit(scheduleBatch).For("UPDATE SKIP LOCKED").Scan(ctx)
		if err != nil || len(rows) == 0 {
			return err
		}
		byTopic := map[string][][]byte{}
		ids := make([]uuid.UUID, len(rows))
		for i, r := range rows {
			byTopic[r.Topic] = append(byTopic[r.Topic], r.Body)
			ids[i] = r.ID
		}
		for topic, bodies := range byTopic {
			if err := emit(ctx, tx, topic, bodies); err != nil {
				return err
			}
		}
		if _, err := tx.NewDelete().Model((*scheduledRow)(nil)).Where("id IN (?)", bun.List(ids)).Exec(ctx); err != nil {
			return err
		}
		n = len(rows)
		return nil
	})
	return n, errors.Wrap(err, "queue: dispatch scheduled jobs")
}

// ScheduledJob 是 queue_scheduled 中等待的任务：执行时间离现在超过 Config.MaxDefer，快到时间时才交给 NSQ。
type ScheduledJob struct {
	ID      uuid.UUID
	Topic   string
	Payload json.RawMessage
	// Metadata 是发布时附带的元数据，例如链路信息。
	Metadata map[string]string
	// AvailableAt 是任务的执行时间，CreatedAt 是写入这张表的时间。
	AvailableAt time.Time
	CreatedAt   time.Time
}

// ScheduledQuery 是 ListScheduled 的条件。
type ScheduledQuery struct {
	// Topic 为空时列出全部 topic。
	Topic string
	// Limit 是最多返回的条数，0 表示 100。
	Limit  int
	Offset int
}

// ListScheduled 按执行时间从早到晚列出 db 中等待的延迟任务，只读。总数见 ReadStatus 的 Scheduled。
func ListScheduled(ctx context.Context, db bun.IDB, q ScheduledQuery) ([]ScheduledJob, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	var rows []scheduledRow
	sel := db.NewSelect().Model(&rows).OrderExpr("s.available_at, s.id").Limit(int64(q.Limit)).Offset(int64(max(q.Offset, 0)))
	if q.Topic != "" {
		sel = sel.Where("s.topic = ?", q.Topic)
	}
	if err := sel.Scan(ctx); err != nil {
		return nil, errors.Wrap(err, "queue: list scheduled jobs")
	}
	jobs := make([]ScheduledJob, len(rows))
	for i, r := range rows {
		jobs[i] = ScheduledJob{ID: r.ID, Topic: r.Topic, AvailableAt: r.AvailableAt, CreatedAt: r.CreatedAt}
		var env envelope
		if json.Unmarshal(r.Body, &env) == nil {
			jobs[i].Payload, jobs[i].Metadata = env.Payload, env.Metadata
		}
	}
	return jobs, nil
}
