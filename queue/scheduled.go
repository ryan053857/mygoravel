package queue

import (
	"context"
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
