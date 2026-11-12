package queue

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Status 是队列在数据库一侧的状态：转发任务的复制槽和 queue_scheduled。NSQ 中积压的任务由 nsqd 统计。
type Status struct {
	Slot string // 复制槽的名字，见 SlotName
	// Exists 报告复制槽是否存在。不存在时没有执行迁移，或者槽在恢复备份、主备切换后丢失，见 RecreateSlot。
	Exists bool
	// Active 报告是否有 Worker 正在转发；ActivePID 是它的复制连接在 PostgreSQL 中的进程号。
	Active    bool
	ActivePID int
	// WALStatus 是 pg_replication_slots.wal_status：reserved、extended 正常；unreserved 表示积压即将超过
	// max_slot_wal_keep_size；lost 表示槽已失效，其中的任务已经丢失。
	WALStatus string
	// Lag 是 Worker 还没确认的 WAL 字节数，即复制槽保留的积压。它包含整个数据库的写入，不只是任务。
	Lag int64
	// Scheduled 是 queue_scheduled 中等待的任务数，NextScheduled 是其中最早的执行时间，没有时为零值。
	Scheduled     int
	NextScheduled time.Time
}

// ReadStatus 读取 db 所在数据库的队列状态。
func ReadStatus(ctx context.Context, db bun.IDB) (Status, error) {
	slot, err := slotOf(ctx, db)
	if err != nil {
		return Status{}, err
	}
	s := Status{Slot: slot}
	var row struct {
		Active    bool           `bun:"active"`
		ActivePID sql.NullInt64  `bun:"active_pid"`
		WALStatus sql.NullString `bun:"wal_status"`
		Lag       sql.NullInt64  `bun:"lag"`
	}
	err = db.NewRaw(`SELECT active, active_pid, wal_status,
		pg_wal_lsn_diff(pg_current_wal_lsn(), confirmed_flush_lsn)::bigint AS lag
		FROM pg_replication_slots WHERE slot_name = ?`, slot).Scan(ctx, &row)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return Status{}, errors.Wrap(err, "queue: read replication slot")
	default:
		s.Exists, s.Active, s.ActivePID = true, row.Active, int(row.ActivePID.Int64)
		s.WALStatus, s.Lag = row.WALStatus.String, row.Lag.Int64
	}
	var next sql.NullTime
	err = db.NewRaw("SELECT count(*), min(available_at) FROM queue_scheduled").Scan(ctx, &s.Scheduled, &next)
	if err != nil {
		return Status{}, errors.Wrap(err, "queue: read queue_scheduled")
	}
	s.NextScheduled = next.Time
	return s, nil
}

// ErrSlotHealthy 表示复制槽存在并且可用，RecreateSlot 没有重建它：重建会丢弃其中还没转发的任务。
var ErrSlotHealthy = errors.New("queue: replication slot is healthy; recreating it would drop pending jobs")

// RecreateSlot 重建 db 所在数据库转发任务的复制槽：槽丢失（恢复备份、没有同步的主备切换）或失效（wal_status 为 lost）之后，
// Worker 拒绝启动，用它恢复。槽存在且可用时返回 ErrSlotHealthy，force 为 true 时仍然重建，丢弃其中还没转发的任务。
// 有 Worker 正在使用槽时失败。
//
// 从丢失到重建期间发布的任务不会被转发，需要从业务数据中找出没有完成的工作重新发布。
func RecreateSlot(ctx context.Context, db bun.IDB, force bool) error {
	s, err := ReadStatus(ctx, db)
	if err != nil {
		return err
	}
	if s.Exists && s.WALStatus != "lost" && !force {
		return ErrSlotHealthy
	}
	if s.Exists {
		if _, err := db.ExecContext(ctx, "SELECT pg_drop_replication_slot(?)", s.Slot); err != nil {
			return errors.Wrap(err, "queue: drop replication slot")
		}
	}
	_, err = db.ExecContext(ctx, "SELECT pg_create_logical_replication_slot(?, 'pgoutput', false, false, true)", s.Slot)
	return errors.Wrap(err, "queue: create replication slot")
}
