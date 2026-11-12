package ratelimit

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Database 把计数存在数据库中，多个实例共享同一份配额，表结构由 Migrations 提供。
// 每个 key 只存一个理论到达时间（tat，Unix 微秒），一次 Allow 是一个短事务：锁住 key 的行，
// 再读取当前时间并按 GCRA 计算，与 Memory 使用同一套算法。时间取应用时钟，各实例的时钟需要同步。
//
// name 最长 255 字节；超过 255 字节的 key 以 SHA-256 摘要保存，各数据库的索引长度都不受 key 影响。
//
// 每次 Allow 都会写数据库，适合登录、验证码、发邮件这类低频操作；
// 每个请求都要经过的全局限流请使用 Memory。
type Database struct {
	gcra
	db   bun.IDB
	name string
}

// maxKeyLength 是 rate_limits 中 name 和 key 列的最大字节数。
const maxKeyLength = 255

type row struct {
	bun.BaseModel `bun:"table:rate_limits,alias:r"`

	Name string `bun:"name,pk"`
	Key  string `bun:"key,pk"`
	TAT  int64  `bun:"tat,notnull"`
}

// NewDatabase 创建数据库限流器。name 区分共用 rate_limits 表的不同限流器，
// 同名的限流器共享配额，因此 name 应与 cfg 一一对应。
// db 必须已执行 rate_limits 迁移；db 为 nil、name 为空或超过 255 字节、cfg 不合法时 panic。
func NewDatabase(db bun.IDB, name string, cfg Config) *Database {
	switch {
	case db == nil:
		panic("ratelimit: NewDatabase: nil database")
	case name == "":
		panic("ratelimit: NewDatabase: empty name")
	case len(name) > maxKeyLength:
		panic("ratelimit: NewDatabase: name longer than 255 bytes")
	}
	return &Database{gcra: newGCRA("NewDatabase", cfg), db: db, name: name}
}

// Allow 实现 Limiter。
func (d *Database) Allow(ctx context.Context, key string) error {
	var wait time.Duration
	err := d.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		// 插入或锁住 key 的行：没有记录时插入 tat=0（与没有记录等价），已有记录时值不变但取得排他行锁。
		// 直接取得排他锁，不经过共享锁：MySQL 上 INSERT IGNORE 遇到重复行时加共享锁，
		// 再 SELECT ... FOR UPDATE 升级为排他锁，并发时会死锁。SQLite 的写事务在 BEGIN 时已独占数据库。
		r := &row{Name: d.name, Key: storedKey(key)}
		q := tx.NewInsert().Model(r)
		if tx.Dialect().Name() == dialect.MySQL {
			q = q.On("DUPLICATE KEY UPDATE").Set("tat = tat")
		} else {
			q = q.On("CONFLICT (name, key) DO UPDATE").Set("tat = r.tat")
		}
		if _, err := q.Exec(ctx); err != nil {
			return err
		}
		if err := tx.NewSelect().Model(r).WherePK().Scan(ctx); err != nil {
			return err
		}
		// 取得锁之后再读取当前时间：若先读时间再等锁，时间较早的调用可能后执行，
		// 看到被其他调用推后的 tat 而被误拒。
		var next time.Time
		next, wait = d.take(time.UnixMicro(r.TAT), time.Now())
		if wait > 0 {
			return nil
		}
		r.TAT = next.UnixMicro()
		_, err := tx.NewUpdate().Model(r).Column("tat").WherePK().Exec(ctx)
		return err
	})
	if err != nil {
		return errors.Wrap(err, "ratelimit: allow")
	}
	if wait > 0 {
		return exceeded(wait)
	}
	return nil
}

// Reset 实现 Limiter：删除 key 的行，与从未出现过的 key 等价。
func (d *Database) Reset(ctx context.Context, key string) error {
	_, err := d.db.NewDelete().Model((*row)(nil)).
		Where("name = ?", d.name).Where("? = ?", bun.Ident("key"), storedKey(key)).Exec(ctx)
	return errors.Wrap(err, "ratelimit: reset")
}

// storedKey 返回 key 在表中保存的形式：不超过 maxKeyLength 时原样保存，否则保存 SHA-256 摘要。
func storedKey(key string) string {
	if len(key) <= maxKeyLength {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// Cleanup 删除最多 limit 条已回满的行，返回删除的条数。由应用定时调用。
//
// tat 已过去的行与不存在的行等价，删除不会改变任何限流结果。
// 清理作用于整张 rate_limits 表，不限于本限流器的 name，因此任意一个 Database 调用即可。
func (d *Database) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("ratelimit: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
	expired := d.db.NewSelect().Model((*row)(nil)).Column("name", "key").
		Where("tat <= ?", time.Now().UnixMicro()).OrderExpr("tat").Limit(int64(limit))
	res, err := d.db.NewDelete().Model((*row)(nil)).
		Where("(name, ?) IN (SELECT name, ? FROM (?) AS expired)", bun.Ident("key"), bun.Ident("key"), expired).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "ratelimit: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "ratelimit: cleanup")
}
