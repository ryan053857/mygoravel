package lock

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// Database 是基于 Bun 的锁存储。Migrations 提供表结构。
// 过期时间按应用服务器时钟计算，各实例的时钟需要同步。
//
// Database 不在后台清理过期的锁：过期的锁会被下一次 Acquire 直接取得，残留行由应用定时调用 Cleanup 删除。
// 锁名最长 MaxNameLength 字节，所有数据库一致。
type Database struct {
	db bun.IDB
}

// MaxNameLength 是 Database 中锁名的最大字节数，与 locks 表 name 列的长度一致。
const MaxNameLength = 255

type row struct {
	bun.BaseModel `bun:"table:locks,alias:l"`

	Name      string    `bun:"name,pk"`
	Owner     string    `bun:"owner,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}

// NewDatabase 创建数据库锁存储。db 必须已执行 locks 迁移。
func NewDatabase(db bun.IDB) *Database {
	if db == nil {
		panic("lock: nil database")
	}
	return &Database{db: db}
}

// Acquire 实现 Store：先插入，name 已存在时只接管已过期的锁。两步各自原子，并发时最多一方成功。
func (s *Database) Acquire(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	if len(name) > MaxNameLength {
		return false, errors.Newf("lock: name longer than %d bytes", MaxNameLength)
	}
	now := time.Now().UTC()
	expires := now.Add(ttl)
	res, err := s.db.NewInsert().Model(&row{Name: name, Owner: owner, ExpiresAt: expires}).Ignore().Exec(ctx)
	if err != nil {
		return false, err
	}
	if ok, err := affected(res); ok || err != nil {
		return ok, err
	}
	res, err = s.db.NewUpdate().Model((*row)(nil)).
		Set("owner = ?", owner).Set("expires_at = ?", expires).
		Where("name = ?", name).Where("expires_at <= ?", now).Exec(ctx)
	if err != nil {
		return false, err
	}
	return affected(res)
}

// Refresh 实现 Store。
func (s *Database) Refresh(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	res, err := s.db.NewUpdate().Model((*row)(nil)).
		Set("expires_at = ?", time.Now().UTC().Add(ttl)).
		Where("name = ?", name).Where("owner = ?", owner).Exec(ctx)
	if err != nil {
		return false, err
	}
	return affected(res)
}

// Release 实现 Store。
func (s *Database) Release(ctx context.Context, name, owner string) (bool, error) {
	res, err := s.db.NewDelete().Model((*row)(nil)).
		Where("name = ?", name).Where("owner = ?", owner).Exec(ctx)
	if err != nil {
		return false, err
	}
	return affected(res)
}

// Held 实现 Store。
func (s *Database) Held(ctx context.Context, name string) (bool, error) {
	return s.db.NewSelect().Model((*row)(nil)).
		Where("name = ?", name).Where("expires_at > ?", time.Now().UTC()).Exists(ctx)
}

// Cleanup 删除最多 limit 条已过期的锁，返回删除的条数。由应用定时调用。
func (s *Database) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("lock: cleanup limit must be positive")
	}
	expired := s.db.NewSelect().Model((*row)(nil)).Column("name").
		Where("expires_at <= ?", time.Now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	res, err := s.db.NewDelete().Model((*row)(nil)).
		Where("name IN (?)", expired).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "lock: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "lock: cleanup")
}

func affected(res interface{ RowsAffected() (int64, error) }) (bool, error) {
	n, err := res.RowsAffected()
	return n > 0, err
}
