package lock

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
)

// DBStore 是基于 Bun 的锁存储。查询兼容 PostgreSQL、MySQL 与 SQLite，Migrations 提供三种数据库的表结构。
// 过期时间按应用服务器时钟计算，各实例的时钟需要同步。
//
// DBStore 不在后台清理过期的锁：过期的锁会被下一次 Acquire 直接取得，残留行由应用定时调用 Cleanup 删除。
// 锁名最长 MaxNameLength 字节，所有数据库一致。
type DBStore struct {
	db bun.IDB
}

// MaxNameLength 是 DBStore 中锁名的最大字节数，与 locks 表 name 列的长度一致。
const MaxNameLength = 255

type row struct {
	bun.BaseModel `bun:"table:locks,alias:l"`

	Name      string    `bun:"name,pk"`
	Owner     string    `bun:"owner,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}

// NewDBStore 创建数据库锁存储。db 必须已执行 locks 迁移。
func NewDBStore(db bun.IDB) *DBStore {
	if db == nil {
		panic("lock: nil database")
	}
	return &DBStore{db: db}
}

// Acquire 实现 Store：先插入，name 已存在时只接管已过期的锁。两步各自原子，并发时最多一方成功。
func (s *DBStore) Acquire(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
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
func (s *DBStore) Refresh(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	res, err := s.db.NewUpdate().Model((*row)(nil)).
		Set("expires_at = ?", time.Now().UTC().Add(ttl)).
		Where("name = ?", name).Where("owner = ?", owner).Exec(ctx)
	if err != nil {
		return false, err
	}
	return affected(res)
}

// Release 实现 Store。
func (s *DBStore) Release(ctx context.Context, name, owner string) (bool, error) {
	res, err := s.db.NewDelete().Model((*row)(nil)).
		Where("name = ?", name).Where("owner = ?", owner).Exec(ctx)
	if err != nil {
		return false, err
	}
	return affected(res)
}

// Cleanup 删除最多 limit 条已过期的锁，返回删除的条数。由应用定时调用。
func (s *DBStore) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("lock: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
	expired := s.db.NewSelect().Model((*row)(nil)).Column("name").
		Where("expires_at <= ?", time.Now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	res, err := s.db.NewDelete().Model((*row)(nil)).
		Where("name IN (SELECT name FROM (?) AS expired)", expired).Exec(ctx)
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
