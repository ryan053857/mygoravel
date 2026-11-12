package queue

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/nsqio/go-nsq"
	"github.com/uptrace/bun"
)

// relayBatch 是一次转发的最多任务数。
const relayBatch = 500

// relay 把 queue_outbox 中到期、或离到期不超过 MaxDefer 的任务转发到 NSQ，直到 ctx 结束。
// 发布任务的事务提交时收到 PostgreSQL 的通知，立即转发；另外每 PollInterval 检查一次。
func (w *Worker) relay(ctx context.Context, producer *nsq.Producer) {
	wake := make(chan struct{}, 1)
	go w.listen(ctx, wake)
	for {
		n, err := w.forward(ctx, producer)
		if err != nil && ctx.Err() == nil {
			w.logger.Error("forward published jobs failed", "error", err)
		}
		if err == nil && n == relayBatch {
			continue // 可能还有，接着转发
		}
		t := time.NewTimer(w.cfg.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// forward 在一个事务中锁住一批任务、发布到 NSQ、删除它们，返回转发的条数。
// 多个 Worker 用 SKIP LOCKED 跳过彼此正在转发的任务。发布成功、提交失败时任务会再转发一次，处理函数本来就需要能重复执行。
func (w *Worker) forward(ctx context.Context, producer *nsq.Producer) (int, error) {
	var n int
	err := w.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := time.Now()
		var rows []outboxRow
		err := tx.NewSelect().Model(&rows).Where("o.available_at <= ?", now.Add(w.cfg.MaxDefer).UTC()).
			OrderExpr("o.id").Limit(relayBatch).For("UPDATE SKIP LOCKED").Scan(ctx)
		if err != nil || len(rows) == 0 {
			return err
		}
		due := map[string][][]byte{}
		ids := make([]int64, len(rows))
		for i, r := range rows {
			ids[i] = r.ID
			if delay := r.AvailableAt.Sub(now); delay > 0 {
				// NSQ 的延迟以毫秒计，向上取整，不早于指定的时间。
				delay = min(delay.Truncate(time.Millisecond)+time.Millisecond, w.cfg.MaxDefer)
				if err := producer.DeferredPublish(w.cfg.Prefix+r.Topic, delay, r.Body); err != nil {
					return errors.Wrapf(err, "publish to %q", r.Topic)
				}
				continue
			}
			due[r.Topic] = append(due[r.Topic], r.Body)
		}
		for topic, bodies := range due {
			if err := producer.MultiPublish(w.cfg.Prefix+topic, bodies); err != nil {
				return errors.Wrapf(err, "publish to %q", topic)
			}
		}
		_, err = tx.NewDelete().Model((*outboxRow)(nil)).Where("id IN (?)", bun.List(ids)).Exec(ctx)
		n = len(rows)
		return err
	})
	return n, errors.Wrap(err, "queue: forward")
}

// listen 在一条专用连接上 LISTEN 发布任务时的通知，每收到一次就唤醒 relay。连接出错时重新连接，
// 期间由 relay 的定时检查兜底。
func (w *Worker) listen(ctx context.Context, wake chan<- struct{}) {
	for ctx.Err() == nil {
		err := w.listenOnce(ctx, wake)
		if ctx.Err() != nil {
			return
		}
		w.logger.Warn("listen for published jobs failed", "error", err)
		wait(ctx, w.cfg.PollInterval)
	}
}

func (w *Worker) listenOnce(ctx context.Context, wake chan<- struct{}) error {
	conn, err := w.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	return conn.Raw(func(driverConn any) error {
		c, ok := driverConn.(*stdlib.Conn)
		if !ok {
			return errors.Newf("queue: unexpected driver connection %T", driverConn)
		}
		pc := c.Conn()
		if _, err := pc.Exec(ctx, "LISTEN "+notifyChannel); err != nil {
			return err
		}
		signal(wake) // LISTEN 之前提交的任务由这一次检查转发
		for {
			if _, err := pc.WaitForNotification(ctx); err != nil {
				return err
			}
			signal(wake)
		}
	})
}

// signal 唤醒 relay；已经有一个待处理的唤醒时什么也不做。
func signal(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

// nsqLogger 把 go-nsq 的日志写进 slog：ERR 开头的记为 Error，其余记为 Warn。
type nsqLogger struct{ logger *slog.Logger }

func (l nsqLogger) Output(_ int, s string) error {
	level := slog.LevelWarn
	if strings.HasPrefix(s, "ERR") {
		level = slog.LevelError
	}
	l.logger.Log(context.Background(), level, "nsq: "+strings.TrimSpace(s))
	return nil
}
