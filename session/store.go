package session

import (
	"context"
	"database/sql"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

// ErrNotFound 表示要撤销的会话不存在、已失效或不属于该主体。
var ErrNotFound = errorx.Define(errorx.NotFound, "session.not_found")

// Store 是基于 Bun 的会话存储，实现 scs.CtxStore 和 scs.IterableCtxStore，
// 查询兼容 PostgreSQL、MySQL 与 SQLite，Migrations 提供三种数据库的表结构。
//
// 每行记录登录主体（类型和 ID）、会话 ID（主体的 Credential）和登录设备，List 据此列出设备，
// Revoke、RevokeOthers 和 RevokeSubject 据此撤销某个主体的会话。
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

	Token            string    `bun:"token,pk"`
	SubjectType      string    `bun:"subject_type,nullzero"`
	SubjectID        string    `bun:"subject_id,nullzero"`
	ID               string    `bun:"id,nullzero"`
	ImpersonatorType string    `bun:"impersonator_type,notnull"`
	ImpersonatorID   string    `bun:"impersonator_id,notnull"`
	IP               string    `bun:"ip,notnull"`
	UserAgent        string    `bun:"user_agent,notnull"`
	LoggedInAt       time.Time `bun:"logged_in_at,nullzero"`
	Data             []byte    `bun:"data,notnull"`
	Expiry           time.Time `bun:"expiry,notnull"`
	RevokedAt        time.Time `bun:"revoked_at,nullzero"`
}

// Session 是一个已登录会话的信息，不包含会话 token。
type Session struct {
	ID           string       // 会话 ID，即登录时主体的 Credential
	Impersonator identity.Ref // 代为登录时实际操作的人，见 identity.Subject.Impersonator；零值表示用户本人
	IP           string
	UserAgent    string
	LoggedInAt   time.Time
	ExpiresAt    time.Time // 当前的过期时间；设置了空闲超时时，每次请求都会向后推迟
	Current      bool      // 是否为 List 传入的主体本次请求所用的会话
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

// CommitCtx 写入会话，以及从会话数据中取出的登录主体、会话 ID 和登录设备；token 已存在时覆盖，覆盖不清除撤销标记。
func (s *Store) CommitCtx(ctx context.Context, token string, b []byte, expiry time.Time) error {
	_, values, err := scs.GobCodec{}.Decode(b)
	if err != nil {
		return errors.Wrap(err, "session: decode")
	}
	sub, _ := values[subjectKey].(identity.Subject)
	l, _ := values[loginKey].(login)
	r := &row{
		Token: token, SubjectType: string(sub.Type), SubjectID: sub.ID, ID: sub.Credential,
		ImpersonatorType: string(sub.Impersonator.Type), ImpersonatorID: sub.Impersonator.ID, IP: l.IP, UserAgent: l.UserAgent, LoggedInAt: l.At, Data: b, Expiry: expiry.UTC(),
	}
	if s.db.Dialect().Name() == dialect.MySQL {
		// MySQL 8 用行别名引用新值，VALUES() 函数已不推荐使用；Bun 的插入构造器不生成行别名。
		_, err = s.db.NewRaw(`INSERT INTO sessions (token, subject_type, subject_id, id, impersonator_type, impersonator_id,
ip, user_agent, logged_in_at, data, expiry)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) AS new
ON DUPLICATE KEY UPDATE subject_type = new.subject_type, subject_id = new.subject_id, id = new.id,
impersonator_type = new.impersonator_type, impersonator_id = new.impersonator_id, ip = new.ip,
user_agent = new.user_agent, logged_in_at = new.logged_in_at, data = new.data, expiry = new.expiry`,
			r.Token, null(r.SubjectType), null(r.SubjectID), null(r.ID), r.ImpersonatorType, r.ImpersonatorID,
			r.IP, r.UserAgent, nullTime(r.LoggedInAt), r.Data, r.Expiry).Exec(ctx)
		return errors.Wrap(err, "session: commit")
	}
	q := s.db.NewInsert().Model(r).On("CONFLICT (token) DO UPDATE")
	for _, col := range []string{"subject_type", "subject_id", "id", "impersonator_type", "impersonator_id", "ip", "user_agent", "logged_in_at", "data", "expiry"} {
		q = q.Set("? = EXCLUDED.?", bun.Ident(col), bun.Ident(col))
	}
	_, err = q.Exec(ctx)
	return errors.Wrap(err, "session: commit")
}

// null 把空字符串转换为 NULL，供原生 SQL 使用，与 nullzero 一致。
func null(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// DeleteCtx 撤销会话；token 不存在时什么也不做。
func (s *Store) DeleteCtx(ctx context.Context, token string) error {
	_, err := s.revoke(ctx).Where("token = ?", token).Exec(ctx)
	return errors.Wrap(err, "session: delete")
}

// RevokeSubject 撤销主体 ref 的全部会话，返回撤销的个数。用于封禁账号、修改权限后强制重新登录等。
func (s *Store) RevokeSubject(ctx context.Context, ref identity.Ref) (int, error) {
	if err := ref.Validate(); err != nil {
		return 0, errors.Wrap(err, "session")
	}
	return affected(s.revoke(ctx).Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).Exec(ctx))
}

// List 返回 sub 未过期、未撤销的会话，最近登录的在前；本次请求所用的会话（sub.Credential）标为 Current。
func (s *Store) List(ctx context.Context, sub identity.Subject) ([]Session, error) {
	if sub.ID == "" {
		return nil, errors.New("session: subject ID is required")
	}
	var rows []row
	err := s.live(s.db.NewSelect().Model(&rows).
		Column("id", "impersonator_type", "impersonator_id", "ip", "user_agent", "logged_in_at", "expiry")).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).OrderExpr("logged_in_at DESC").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "session: list")
	}
	out := make([]Session, 0, len(rows))
	for _, r := range rows {
		out = append(out, Session{
			ID: r.ID, Impersonator: identity.Ref{Type: identity.SubjectType(r.ImpersonatorType), ID: r.ImpersonatorID}, IP: r.IP, UserAgent: r.UserAgent,
			LoggedInAt: r.LoggedInAt, ExpiresAt: r.Expiry, Current: r.ID != "" && r.ID == sub.Credential,
		})
	}
	return out, nil
}

// Revoke 撤销 sub 的会话 id，例如在设备列表中退出某一台设备。会话不存在、已失效或不属于 sub 时返回 ErrNotFound。
func (s *Store) Revoke(ctx context.Context, sub identity.Subject, id string) error {
	if sub.ID == "" || id == "" {
		return ErrNotFound.New()
	}
	n, err := affected(s.revoke(ctx).Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).Where("id = ?", id).Exec(ctx))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound.New()
	}
	return nil
}

// RevokeOthers 撤销 sub 除本次请求所用会话（sub.Credential）以外的全部会话，返回撤销的个数，例如“退出其他设备”。
// 在 API 中调用时 sub.Credential 是 API 令牌的 ID，不是任何会话，因此撤销全部会话。
func (s *Store) RevokeOthers(ctx context.Context, sub identity.Subject) (int, error) {
	if sub.ID == "" {
		return 0, errors.New("session: subject ID is required")
	}
	q := s.revoke(ctx).Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID)
	if sub.Credential != "" {
		q = q.Where("(id IS NULL OR id <> ?)", sub.Credential)
	}
	return affected(q.Exec(ctx))
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
