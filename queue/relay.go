package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/nsqio/go-nsq"
)

const (
	// relayBatch 是一次发给 NSQ 的最多任务数。
	relayBatch = 500
	// relayIdle 是攒批的最长等待：这么久没有新的任务到达，就把已经收到的发出去。
	relayIdle = 2 * time.Millisecond
	// standbyInterval 是向 PostgreSQL 报告确认位置的间隔。它必须小于 wal_sender_timeout，否则连接被断开。
	standbyInterval = 3 * time.Second
)

// relayRetry 是复制槽被其他 Worker 占用、或连接出错之后重试的间隔，也是转发的 Worker 停止后备用的接手所需的最长时间。
var relayRetry = 5 * time.Second

// sqlstateObjectInUse 是复制槽正被另一个连接使用时 START_REPLICATION 返回的错误码。
const sqlstateObjectInUse = "55006"

// relayed 是从 WAL 中读到的一个任务。
type relayed struct {
	topic string
	body  []byte
}

// relay 经由逻辑复制读取已提交事务中的任务，转发到 NSQ，直到 ctx 结束。
//
// 一个复制槽同时只能被一个连接使用：多个 Worker 中只有一个在转发，其余的每 relayRetry 尝试一次，
// 转发的 Worker 停止或断开后由其中一个接手，从槽中已确认的位置继续。
func (w *Worker) relay(ctx context.Context, producer *nsq.Producer) {
	standby := false
	for ctx.Err() == nil {
		err := w.stream(ctx, producer)
		if ctx.Err() != nil {
			return
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == sqlstateObjectInUse {
			if !standby {
				w.logger.Info("another worker is relaying jobs, standing by", "slot", w.slot)
			}
			standby = true
		} else {
			standby = false
			w.logger.Error("relay jobs failed", "slot", w.slot, "error", err)
		}
		wait(ctx, relayRetry)
	}
}

// stream 建立复制连接，持续转发，直到 ctx 结束或出错。
//
// 任务按事务提交的顺序到达。一个事务的任务全部收到（Commit）之后才会发出；发到 NSQ 之后，才向 PostgreSQL 确认
// 这个事务的位置。连接中断时，没有确认的事务会被再次读到，任务可能重复投递，与处理函数需要能重复执行的约定一致。
func (w *Worker) stream(ctx context.Context, producer *nsq.Producer) error {
	cfg, err := replicationConfig(ctx, w.db, w.cfg.ReplicationDSN)
	if err != nil {
		return err
	}
	conn, err := pgconn.ConnectConfig(ctx, cfg)
	if err != nil {
		return errors.Wrap(err, "queue: replication connection")
	}
	defer func() { _ = conn.Close(context.Background()) }()
	err = pglogrepl.StartReplication(ctx, conn, w.slot, 0, pglogrepl.StartReplicationOptions{
		PluginArgs: []string{"proto_version '1'", "publication_names '" + Publication + "'", "messages 'true'"},
	})
	if err != nil {
		return errors.Wrap(err, "queue: start replication")
	}
	w.logger.Info("relaying jobs", "slot", w.slot)
	w.relaying.Store(true)
	defer w.relaying.Store(false)

	var (
		open, ready          []relayed // 当前事务中的任务；已提交、还没发出的任务
		committed, confirmed pglogrepl.LSN
		nextReport           = time.Now().Add(standbyInterval)
	)
	report := func(ctx context.Context) error {
		nextReport = time.Now().Add(standbyInterval)
		err := pglogrepl.SendStandbyStatusUpdate(ctx, conn, pglogrepl.StandbyStatusUpdate{WALWritePosition: confirmed})
		return errors.Wrap(err, "queue: report replication position")
	}
	flush := func(ctx context.Context) error {
		if len(ready) > 0 {
			if err := w.forward(ctx, producer, ready); err != nil {
				return err
			}
			ready = ready[:0]
		}
		if committed > confirmed {
			confirmed = committed
			return report(ctx)
		}
		return nil
	}
	for {
		deadline := nextReport
		if idle := time.Now().Add(relayIdle); len(ready) > 0 && idle.Before(deadline) {
			deadline = idle
		}
		rctx, cancel := context.WithDeadline(ctx, deadline)
		raw, err := conn.ReceiveMessage(rctx)
		cancel()
		if ctx.Err() != nil {
			// 停止时发出已经收到的任务，尽量不让接手的 Worker 重复投递。
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = flush(stopCtx)
			return nil
		}
		if err != nil {
			if !pgconn.Timeout(err) {
				return errors.Wrap(err, "queue: receive replication message")
			}
			if err := flush(ctx); err != nil {
				return err
			}
			if !time.Now().Before(nextReport) {
				if err := report(ctx); err != nil {
					return err
				}
			}
			continue
		}
		switch m := raw.(type) {
		case *pgproto3.ErrorResponse:
			return errors.Wrap(pgconn.ErrorResponseToPgError(m), "queue: replication")
		case *pgproto3.CopyData:
			if len(m.Data) == 0 {
				continue
			}
			switch m.Data[0] {
			case pglogrepl.PrimaryKeepaliveMessageByteID:
				pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(m.Data[1:])
				if err != nil {
					return errors.Wrap(err, "queue: parse keepalive")
				}
				if pkm.ReplyRequested {
					if err := report(ctx); err != nil {
						return err
					}
				}
			case pglogrepl.XLogDataByteID:
				xld, err := pglogrepl.ParseXLogData(m.Data[1:])
				if err != nil {
					return errors.Wrap(err, "queue: parse WAL data")
				}
				msg, err := pglogrepl.Parse(xld.WALData)
				if err != nil {
					return errors.Wrap(err, "queue: decode WAL data")
				}
				switch msg := msg.(type) {
				case *pglogrepl.BeginMessage:
					open = open[:0]
				case *pglogrepl.LogicalDecodingMessage:
					if topic, ok := strings.CutPrefix(msg.Prefix, MessagePrefix); ok {
						// Content 引用接收缓冲区，下一次接收时会被覆盖。
						open = append(open, relayed{topic: topic, body: bytes.Clone(msg.Content)})
					}
				case *pglogrepl.CommitMessage:
					ready = append(ready, open...)
					open = open[:0]
					committed = msg.TransactionEndLSN
					if len(ready) >= relayBatch {
						if err := flush(ctx); err != nil {
							return err
						}
					}
				}
			}
		}
	}
}

// forward 把任务交给 NSQ：已经到时间的成批发布；不超过 MaxDefer 的作为 NSQ 的延迟消息；
// 更晚的写入 queue_scheduled，快到时间时再写回 WAL。
func (w *Worker) forward(ctx context.Context, producer *nsq.Producer, jobs []relayed) error {
	now := time.Now()
	due := map[string][][]byte{}
	var later []scheduledRow
	for _, j := range jobs {
		var head struct {
			ID          uuid.UUID `json:"id"`
			AvailableAt time.Time `json:"available_at"`
		}
		if err := json.Unmarshal(j.body, &head); err != nil {
			w.logger.Error("drop malformed job", "topic", j.topic, "error", err)
			continue
		}
		switch delay := head.AvailableAt.Sub(now); {
		case delay <= 0:
			due[j.topic] = append(due[j.topic], j.body)
		case delay <= w.cfg.MaxDefer:
			// NSQ 的延迟以毫秒计，向上取整，不早于指定的时间。
			delay = min(delay.Truncate(time.Millisecond)+time.Millisecond, w.cfg.MaxDefer)
			if err := producer.DeferredPublish(w.cfg.Prefix+j.topic, delay, j.body); err != nil {
				return errors.Wrapf(err, "queue: publish to %q", j.topic)
			}
			w.countRelayed(ctx, routeDeferred, 1)
		default:
			later = append(later, scheduledRow{ID: head.ID, Topic: j.topic, Body: j.body, AvailableAt: head.AvailableAt.UTC(), CreatedAt: now.UTC()})
		}
	}
	for topic, bodies := range due {
		if err := producer.MultiPublish(w.cfg.Prefix+topic, bodies); err != nil {
			return errors.Wrapf(err, "queue: publish to %q", topic)
		}
		w.countRelayed(ctx, routePublished, len(bodies))
	}
	if err := schedule(ctx, w.db, later); err != nil {
		return err
	}
	w.countRelayed(ctx, routeScheduled, len(later))
	return nil
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
