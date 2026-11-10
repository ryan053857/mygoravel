package session

import (
	"context"
	"database/sql"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/ryan053857/mygoravel/identity"
)

// Store 是基于 Bun 的会话存储，实现 scs.CtxStore 和 scs.IterableCtxStore，
// 查询兼容 PostgreSQL、MySQL 与 SQLite，Migrations 提供 PostgreSQL 与 SQLite 的表结构。
//
// 每行记录登录主体的 ID，RevokeSubject 和 RevokeOthers 据此撤销某个主体的会话。
// 删除和撤销只给行打上 revoked_at 标记，读取时跳过：撤销前已在处理中的请求结束时会再次提交会话，
// 若真的删除行，这次提交会把会话重新插入；而提交不会清除标记，被撤销的会话不会复活。
// 标记行在过期后由 Cleanup 删除。
//
// Store 从会话数据中解码主体，要求会话管理器使用 scs 默认的 gob 编码，session.New 创建的即是如此。
type Store struct {
	db bun.IDB
}

type row struct {
	bun.BaseModel `bun:"table:sessions,alias:s"`

	Token     string    `bun:"token,pk"`
	SubjectID string    `bun:"subject_id,nullzero"`
	Data      []byte    `bun:"data,notnull"`
	Expiry    time.Time `bun:"expiry,notnull"`
	RevokedAt time.Time `bun:"revoked_at,nullzero"`
}

// NewStore 创建会话存储。db 必须已执行 sessions 迁移。
func NewStore(db bun.IDB) *Store {
	if db == nil {
		panic("session: nil database")
	}
	return &Store{db: db}
}

// FindCtx 返回未过期且未撤销的会话数据。
func (s *Store) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	var r row
	err := s.live(s.db.NewSelect().Model(&r).Column("data").Where("token = ?", token)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, errors.Wrap(err, "session: find")
	}
	return r.Data, true, nil
}

// CommitCtx 写入会话及其登录主体的 ID，token 已存在时覆盖；覆盖不清除撤销标记。
func (s *Store) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	_, values, err := scs.GobCodec{}.Decode(b)
	if err != nil {
		return errors.Wrap(err, "session: decode")
	}
	sub, _ := values[subjectKey].(identity.Subject)
	r := &row{Token: token, SubjectID: sub.ID, Data: b, Expiry: expiry.UTC()}
	q := s.db.NewInsert().Model(r)
	if s.db.Dialect().Name() == dialect.MySQL {
		q = q.On("DUPLICATE KEY UPDATE").
			Set("subject_id = VALUES(subject_id)").Set("data = VALUES(data)").Set("expiry = VALUES(expiry)")
	} else {
		q = q.On("CONFLICT (token) DO UPDATE").
			Set("subject_id = EXCLUDED.subject_id").Set("data = EXCLUDED.data").Set("expiry = EXCLUDED.expiry")
	}
	_, err = q.Exec(ctx)
	return errors.Wrap(err, "session: commit")
}

// DeleteCtx 撤销会话；token 不存在时什么也不做。
func (s *Store) DeleteCtx(ctx context.Context, token string) error {
	_, err := s.revoke(ctx).Where("token = ?", token).Exec(ctx)
	return errors.Wrap(err, "session: delete")
}

// RevokeSubject 撤销 subjectID 的全部会话，返回撤销的个数。用于封禁账号、修改权限后强制重新登录等。
func (s *Store) RevokeSubject(ctx context.Context, subjectID string) (int, error) {
	if subjectID == "" {
		return 0, errors.New("session: subject ID is required")
	}
	return affected(s.revoke(ctx).Where("subject_id = ?", subjectID).Exec(ctx))
}

// RevokeOthers 撤销当前登录主体除本会话外的全部会话，返回撤销的个数，例如“退出其他设备”。
// 当前会话未登录时返回错误。
func (s *Store) RevokeOthers(ctx context.Context, sm *scs.SessionManager) (int, error) {
	sub, ok := Subject(ctx, sm)
	if !ok {
		return 0, errors.New("session: not logged in")
	}
	return affected(s.revoke(ctx).Where("subject_id = ?", sub.ID).Where("token <> ?", sm.Token(ctx)).Exec(ctx))
}

// revoke 返回给未撤销、未过期的行打上撤销标记的更新，调用方补充条件。
func (s *Store) revoke(ctx context.Context) *bun.UpdateQuery {
	now := time.Now().UTC()
	return s.db.NewUpdate().Model((*row)(nil)).Set("revoked_at = ?", now).
		Where("revoked_at IS NULL").Where("expiry > ?", now)
}

// live 把查询限定在未撤销、未过期的行。
func (s *Store) live(q *bun.SelectQuery) *bun.SelectQuery {
	return q.Where("revoked_at IS NULL").Where("expiry > ?", time.Now().UTC())
}

func affected(res sql.Result, err error) (int, error) {
	if err != nil {
		return 0, errors.Wrap(err, "session: revoke")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "session: revoke")
}

// AllCtx 返回全部未过期且未撤销的会话，供 scs.SessionManager.Iterate 使用。
func (s *Store) AllCtx(ctx context.Context) (map[string][]byte, error) {
	var rows []row
	if err := s.live(s.db.NewSelect().Model(&rows).Column("token", "data")).Scan(ctx); err != nil {
		return nil, errors.Wrap(err, "session: all")
	}
	all := make(map[string][]byte, len(rows))
	for _, r := range rows {
		all[r.Token] = r.Data
	}
	return all, nil
}

// Cleanup 删除最多 limit 条已过期的会话（包括已撤销的），返回删除的条数。由应用定时调用。
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
