package realtime

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

var _ PresenceStore = (*DatabasePresence)(nil)

// DatabasePresence 把在线记录保存在 realtime_presence 表中，PostgreSQL、MySQL 和 SQLite 通用，
// Migrations 提供三种数据库的表结构。过期时间按应用服务器时钟计算，各实例的时钟需要同步。
//
// 每个连接在每个 presence 频道中每 25 秒续期一次，也就是一次写入：presence 订阅很多时改用 redispresence。
// 过期的行不会被 List 返回，残留行由应用定时调用 Cleanup 删除。
type DatabasePresence struct {
	db bun.IDB
}

type presenceRow struct {
	bun.BaseModel `bun:"table:realtime_presence,alias:rp"`

	Channel   string    `bun:"channel,pk"`
	ClientID  string    `bun:"client_id,pk"`
	SubjectID string    `bun:"subject_id,notnull"`
	Info      []byte    `bun:"info,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}

// NewDatabasePresence 创建基于数据库的 PresenceStore。db 必须已执行 realtime 的迁移，为 nil 时 panic。
func NewDatabasePresence(db bun.IDB) *DatabasePresence {
	if db == nil {
		panic("realtime: nil database")
	}
	return &DatabasePresence{db: db}
}

// Add 实现 PresenceStore：续期是最常见的情况，所以先更新，没有这一行时再插入。
func (s *DatabasePresence) Add(ctx context.Context, e PresenceEntry, ttl time.Duration) error {
	info := e.Info
	if info == nil {
		info = []byte{}
	}
	expires := time.Now().UTC().Add(ttl)
	res, err := s.db.NewUpdate().Model((*presenceRow)(nil)).
		Set("subject_id = ?", e.Subject).Set("info = ?", info).Set("expires_at = ?", expires).
		Where("channel = ?", e.Channel).Where("client_id = ?", e.Client).Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "realtime: update presence")
	}
	if n, err := res.RowsAffected(); err != nil || n > 0 {
		return errors.Wrap(err, "realtime: update presence")
	}
	// 并发的两次 Add 都可能走到这里，Ignore 让后到的一次不报主键冲突。
	_, err = s.db.NewInsert().Model(&presenceRow{
		Channel: e.Channel, ClientID: e.Client, SubjectID: e.Subject, Info: info, ExpiresAt: expires,
	}).Ignore().Exec(ctx)
	return errors.Wrap(err, "realtime: insert presence")
}

// Remove 实现 PresenceStore。
func (s *DatabasePresence) Remove(ctx context.Context, channel, client string) error {
	_, err := s.db.NewDelete().Model((*presenceRow)(nil)).
		Where("channel = ?", channel).Where("client_id = ?", client).Exec(ctx)
	return errors.Wrap(err, "realtime: delete presence")
}

// List 实现 PresenceStore。
func (s *DatabasePresence) List(ctx context.Context, channel string) ([]PresenceEntry, error) {
	var rows []presenceRow
	err := s.db.NewSelect().Model(&rows).
		Where("channel = ?", channel).Where("expires_at > ?", time.Now().UTC()).Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "realtime: select presence")
	}
	out := make([]PresenceEntry, len(rows))
	for i, r := range rows {
		out[i] = PresenceEntry{Channel: r.Channel, Client: r.ClientID, Subject: r.SubjectID, Info: r.Info}
		if len(r.Info) == 0 {
			out[i].Info = nil
		}
	}
	return out, nil
}

// Cleanup 删除最多 limit 条已过期的记录，返回删除的条数。可以交给 schedule.Cleanup 定期执行。
func (s *DatabasePresence) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("realtime: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能使用。
	expired := s.db.NewSelect().Model((*presenceRow)(nil)).Column("channel", "client_id").
		Where("expires_at <= ?", time.Now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	res, err := s.db.NewDelete().Model((*presenceRow)(nil)).
		Where("(channel, client_id) IN (SELECT channel, client_id FROM (?) AS expired)", expired).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "realtime: cleanup presence")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "realtime: cleanup presence")
}
