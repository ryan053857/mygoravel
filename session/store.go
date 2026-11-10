package session

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Store 是基于 Bun 的会话存储，实现 scs.CtxStore 和 scs.IterableCtxStore，
// 查询兼容 PostgreSQL、MySQL 与 SQLite，Migrations 提供 PostgreSQL 与 SQLite 的表结构。
//
// Store 不在后台清理过期会话：过期会话读不到，由应用定时调用 Cleanup 删除。
type Store struct {
	db bun.IDB
}

type row struct {
	bun.BaseModel `bun:"table:sessions,alias:s"`

	Token  string    `bun:"token,pk"`
	Data   []byte    `bun:"data,notnull"`
	Expiry time.Time `bun:"expiry,notnull"`
}

// NewStore 创建会话存储。db 必须已执行 sessions 迁移。
func NewStore(db bun.IDB) *Store {
	if db == nil {
		panic("session: nil database")
	}
	return &Store{db: db}
}

// FindCtx 返回未过期会话的数据。
func (s *Store) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var r row
	err := s.db.NewSelect().Model(&r).Column("data").
		Where("token = ?", token).Where("expiry > ?", time.Now().UTC()).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.Wrap(err, "session: find")
	}
	return r.Data, true, nil
}

// CommitCtx 写入会话，token 已存在时覆盖数据和过期时间。
func (s *Store) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	r := &row{Token: token, Data: b, Expiry: expiry.UTC()}
	q := s.db.NewInsert().Model(r)
	if s.db.Dialect().Name() == dialect.MySQL {
		q = q.On("DUPLICATE KEY UPDATE").Set("data = VALUES(data)").Set("expiry = VALUES(expiry)")
	} else {
		q = q.On("CONFLICT (token) DO UPDATE").Set("data = EXCLUDED.data").Set("expiry = EXCLUDED.expiry")
	}
	_, err := q.Exec(ctx)
	return errors.Wrap(err, "session: commit")
}

// DeleteCtx 删除会话；token 不存在时什么也不做。
func (s *Store) DeleteCtx(ctx context.Context, token string) error {
	_, err := s.db.NewDelete().Model((*row)(nil)).Where("token = ?", token).Exec(ctx)
	return errors.Wrap(err, "session: delete")
}

// AllCtx 返回全部未过期会话，供 scs.SessionManager.Iterate 使用。
func (s *Store) AllCtx(ctx context.Context) (map[string][]byte, error) {
	var rows []row
	if err := s.db.NewSelect().Model(&rows).Column("token", "data").
		Where("expiry > ?", time.Now().UTC()).Scan(ctx); err != nil {
		return nil, errors.Wrap(err, "session: all")
	}
	all := make(map[string][]byte, len(rows))
	for _, r := range rows {
		all[r.Token] = r.Data
	}
	return all, nil
}

// Cleanup 删除最多 limit 条已过期的会话，返回删除的条数。由应用定时调用。
func (s *Store) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("session: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
	expired := s.db.NewSelect().Model((*row)(nil)).Column("token").
		Where("expiry <= ?", time.Now().UTC()).OrderExpr("expiry").Limit(int64(limit))
	res, err := s.db.NewDelete().Model((*row)(nil)).
		Where("token IN (SELECT token FROM (?) AS expired)", expired).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "session: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "session: cleanup")
}

// Find 实现 scs.Store；scs 优先调用 FindCtx。
func (s *Store) Find(token string) ([]byte, bool, error) {
	return s.FindCtx(context.Background(), token)
}

// Commit 实现 scs.Store；scs 优先调用 CommitCtx。
func (s *Store) Commit(token string, b []byte, expiry time.Time) error {
	return s.CommitCtx(context.Background(), token, b, expiry)
}

// Delete 实现 scs.Store；scs 优先调用 DeleteCtx。
func (s *Store) Delete(token string) error {
	return s.DeleteCtx(context.Background(), token)
}
