package broadcast

import (
	"context"
	"log/slog"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/logging"
)

// Database 是轮询 broadcasts 表的 Backend，支持 PostgreSQL、MySQL 和 SQLite，Migrations 提供三种数据库的表结构。
//
// Publish 插入一行；每个实例每 100 毫秒按 id 读取新行，所以消息最多延迟一个轮询间隔。
// 自增 id 的提交顺序可能与分配顺序不同：读到不连续的 id 时，等待缺失的行最多 2 秒，
// 期间到达就按顺序投递，超时则视为回滚留下的空号跳过。数据库暂时不可用时从上次读到的位置继续，
// 不会丢消息，所以 Database 不会让订阅因 ErrLost 结束。
//
// 表中的行由应用定时调用 Cleanup 删除。
type Database struct {
	db     bun.IDB
	logger *slog.Logger

	interval   time.Duration
	gapTimeout time.Duration
	retention  time.Duration
}

type row struct {
	bun.BaseModel `bun:"table:broadcasts,alias:b"`

	ID        int64     `bun:"id,pk,autoincrement"`
	Topic     string    `bun:"topic,notnull"`
	Data      []byte    `bun:"data,notnull"`
	CreatedAt time.Time `bun:"created_at,notnull"`
}

// batchSize 是每次轮询最多读取的行数；读满时立即再读，不等下一个间隔。
const batchSize = 1000

// NewDatabase 创建数据库 Backend。db 必须已执行 broadcasts 迁移。logger 记录轮询失败与恢复，
// nil 时使用 slog.Default()。db 为 nil 时 panic。
func NewDatabase(db bun.IDB, logger *slog.Logger) *Database {
	if db == nil {
		panic("broadcast: nil database")
	}
	return &Database{
		db:         db,
		logger:     logging.Or(logger).With(logging.AttrComponent, "broadcast"),
		interval:   100 * time.Millisecond,
		gapTimeout: 2 * time.Second,
		retention:  10 * time.Minute,
	}
}

// Publish 实现 Backend：插入一行。
func (d *Database) Publish(ctx context.Context, msg Message) error {
	data := msg.Data
	if data == nil {
		data = []byte{}
	}
	_, err := d.db.NewInsert().Model(&row{Topic: msg.Topic, Data: data, CreatedAt: time.Now().UTC()}).Exec(ctx)
	return err
}

// Listen 实现 Backend：从开始监听时的最新一行之后读起。
func (d *Database) Listen(ctx context.Context, deliver func(Message), _ func(error)) {
	var cursor int64
	for {
		err := d.db.NewSelect().Model((*row)(nil)).ColumnExpr("COALESCE(MAX(id), 0)").Scan(ctx, &cursor)
		if err == nil {
			break
		}
		if !d.failed(ctx, err) {
			return
		}
	}
	var (
		failing  bool
		gapSince time.Time
	)
	for {
		var rows []row
		err := d.db.NewSelect().Model(&rows).Where("id > ?", cursor).OrderExpr("id").Limit(batchSize).Scan(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if !failing {
				d.logger.Error("broadcast poll failed; retrying", "error", err)
				failing = true
			}
		} else if failing {
			d.logger.Info("broadcast poll recovered")
			failing = false
		}
		for _, r := range rows {
			if r.ID != cursor+1 {
				if gapSince.IsZero() {
					gapSince = time.Now()
				}
				if time.Since(gapSince) < d.gapTimeout {
					break // 等缺失的行提交
				}
			}
			gapSince = time.Time{}
			deliver(Message{Topic: r.Topic, Data: r.Data})
			cursor = r.ID
		}
		if len(rows) == batchSize && gapSince.IsZero() {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.interval):
		}
	}
}

// failed 记录 Listen 开始时的错误并等待重试，ctx 结束时返回 false。
func (d *Database) failed(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	d.logger.Error("broadcast listen failed; retrying", "error", err)
	select {
	case <-ctx.Done():
		return false
	case <-time.After(time.Second):
		return true
	}
}

// Cleanup 删除 10 分钟以前的行，每次最多 limit 行，返回删除的行数。可以交给 schedule.Cleanup 定期执行。
func (d *Database) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("broadcast: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
	old := d.db.NewSelect().Model((*row)(nil)).Column("id").
		Where("created_at < ?", time.Now().UTC().Add(-d.retention)).OrderExpr("id").Limit(int64(limit))
	res, err := d.db.NewDelete().Model((*row)(nil)).
		Where("id IN (SELECT id FROM (?) AS old)", old).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "broadcast: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "broadcast: cleanup")
}
