package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"strings"
	"time"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

var (
	// ErrInvalid 表示 token 不存在、已撤销或已过期。rest.RequireBearer 把它呈现为 401。
	ErrInvalid = errorx.Define(errorx.Unauthenticated, "apitoken.invalid")
	// ErrNotFound 表示要撤销的 token 不存在或不属于该主体。
	ErrNotFound = errorx.Define(errorx.NotFound, "apitoken.not_found")
)

// Kind 是 token 的类别。
type Kind string

const (
	// KindLogin 是 SPA、App 登录时签发的 token，按 Config 滑动过期，代表一台设备。
	KindLogin Kind = "login"
	// KindPersonal 是个人访问令牌，供脚本和第三方集成使用，过期时间由创建者指定。
	KindPersonal Kind = "personal"
)

// Device 描述登录 token 所在的设备，用于设备列表。
type Device struct {
	// Remember 表示登录时选择了“记住我”，空闲时长使用 Config.RememberIdleTimeout。
	Remember bool
	// IP 是登录请求的客户端地址。
	IP string
	// UserAgent 是登录请求的 User-Agent，超过 255 个字符时截断。
	UserAgent string
}

// Token 是已签发 token 的信息，不包含 token 本身。
type Token struct {
	ID           string
	Kind         Kind
	Name         string       // 个人访问令牌的名称；登录 token 为空
	Scopes       []string     // 个人访问令牌的权限限制；登录 token 为空
	Tenant       string       // token 绑定的租户；"" 表示不绑定
	Impersonator identity.Ref // 代为登录时签发 token 的实际操作者，见 identity.Subject.Impersonator；零值表示用户本人
	IP           string
	UserAgent    string
	CreatedAt    time.Time
	LastUsedAt   time.Time
	ExpiresAt    time.Time // 零值表示不过期
	Current      bool      // 是否为 List 传入的主体本次请求所用的 token
}

// touchInterval 是 Validate 两次写入 last_used_at 的最小间隔：每个 token 每分钟最多写一次库，
// 空闲过期的精度相应为一分钟。
const touchInterval = time.Minute

const (
	maxName      = 255
	maxUserAgent = 255
)

// Store 是基于 Bun 的 token 存储，查询兼容 PostgreSQL、MySQL 与 SQLite，
// Migrations 提供三种数据库的表结构。可以并发使用。
//
// Store 不在后台清理过期 token：过期 token 校验不通过，由应用定时调用 Cleanup 删除。
type Store struct {
	db  bun.IDB
	cfg Config
	now func() time.Time
}

type row struct {
	bun.BaseModel `bun:"table:api_tokens,alias:t"`

	ID               uuid.UUID `bun:"id,pk,type:uuid"`
	TokenHash        string    `bun:"token_hash,notnull"`
	SubjectType      string    `bun:"subject_type,notnull"`
	SubjectID        string    `bun:"subject_id,notnull"`
	Tenant           string    `bun:"tenant,notnull"`
	ImpersonatorType string    `bun:"impersonator_type,notnull"`
	ImpersonatorID   string    `bun:"impersonator_id,notnull"`
	Kind             Kind      `bun:"kind,notnull"`
	Name             string    `bun:"name,notnull"`
	Scopes           string    `bun:"scopes,notnull"`
	Remember         bool      `bun:"remember,notnull"`
	IP               string    `bun:"ip,notnull"`
	UserAgent        string    `bun:"user_agent,notnull"`
	CreatedAt        time.Time `bun:"created_at,notnull"`
	LastUsedAt       time.Time `bun:"last_used_at,notnull"`
	ExpiresAt        time.Time `bun:"expires_at,nullzero"`
}

// NewStore 创建 token 存储。db 必须已执行 api_tokens 迁移。cfg 不合法或 db 为 nil 时 panic。
func NewStore(db bun.IDB, cfg Config) *Store {
	if db == nil {
		panic("apitoken: nil database")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "apitoken"))
	}
	return &Store{db: db, cfg: cfg, now: time.Now}
}

// Login 为 sub 签发登录 token，返回 token 明文，明文只在这里返回一次。有效期按 Config 滑动。
//
// 登录 token 代表用户本人，不限制权限：校验得到的主体 Scopes 为 nil，权限由 Resolve 按用户当前的角色加载。
// sub.Tenant 不为空时，token 只能在这个租户中使用，例如只属于某个租户的终端客户账号。
// sub.Impersonator 不为零值时签发的是代为登录的 token，校验得到的主体带有同样的 Impersonator。
func (s *Store) Login(ctx context.Context, sub identity.Subject, d Device) (string, error) {
	now := s.now().UTC()
	r := &row{
		Kind:      KindLogin,
		Tenant:    sub.Tenant,
		Remember:  d.Remember,
		IP:        d.IP,
		UserAgent: truncate(d.UserAgent, maxUserAgent),
		ExpiresAt: s.cfg.deadline(now, now, d.Remember),
	}
	return s.issue(ctx, sub, r, now)
}

// Personal 描述一个个人访问令牌。
type Personal struct {
	// Name 用于在令牌列表中辨认，长度为 1 到 255 个字符。
	Name string
	// Scopes 是令牌被限制在内的权限，至少一个，支持通配符（见 identity.Subject.Can）。
	// 令牌实际能做的是用户当前权限与 Scopes 的交集，用户被收回权限后令牌随之收窄。
	Scopes []string
	// Tenant 不为空时，令牌只能在这个租户中使用。
	Tenant string
	// ExpiresAt 为零值表示不过期，否则必须晚于当前时间。
	ExpiresAt time.Time
}

// Create 为 sub 创建个人访问令牌，返回 token 明文，明文只在这里返回一次。
// 代为登录期间（sub.Impersonator 不为零值）创建的令牌记录 Impersonator，之后每次使用都归于这个操作者。
func (s *Store) Create(ctx context.Context, sub identity.Subject, p Personal) (string, error) {
	now := s.now().UTC()
	name := strings.TrimSpace(p.Name)
	if name == "" || len([]rune(name)) > maxName {
		return "", errors.Newf("apitoken: name must be 1 to %d characters", maxName)
	}
	if len(p.Scopes) == 0 {
		return "", errors.New("apitoken: a personal access token needs at least one scope")
	}
	if !p.ExpiresAt.IsZero() && !p.ExpiresAt.After(now) {
		return "", errors.New("apitoken: ExpiresAt must be in the future")
	}
	joined, err := joinScopes(p.Scopes)
	if err != nil {
		return "", err
	}
	r := &row{Kind: KindPersonal, Name: name, Scopes: joined, Tenant: p.Tenant, ExpiresAt: p.ExpiresAt.UTC()}
	return s.issue(ctx, sub, r, now)
}

func (s *Store) issue(ctx context.Context, sub identity.Subject, r *row, now time.Time) (string, error) {
	if err := sub.Ref().Validate(); err != nil {
		return "", errors.Wrap(err, "apitoken")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return "", errors.Wrap(err, "apitoken: new id")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.Wrap(err, "apitoken: random")
	}
	plain := base64.RawURLEncoding.EncodeToString(b)
	r.ID, r.TokenHash = id, hash(plain)
	r.SubjectType, r.SubjectID = string(sub.Type), sub.ID
	r.ImpersonatorType, r.ImpersonatorID = string(sub.Impersonator.Type), sub.Impersonator.ID
	r.CreatedAt, r.LastUsedAt = now, now
	if _, err := s.db.NewInsert().Model(r).Exec(ctx); err != nil {
		return "", errors.Wrap(err, "apitoken: insert")
	}
	return plain, nil
}

// Validate 实现 rest.TokenValidator：返回 token 代表的主体，Method 为 bearer，Credential 为 token 的 ID，
// Tenant 为 token 绑定的租户，Impersonator 为签发时的代为登录者。登录 token 的 Scopes 为 nil，个人访问令牌的 Scopes 是创建时的限制；
// 两者的 Permissions 都为空，由 Resolve 加载。token 不存在、已撤销或已过期时返回 ErrInvalid。
//
// 距上次记录使用超过一分钟时，更新最近使用时间，并延长登录 token 的有效期。
func (s *Store) Validate(ctx context.Context, token string) (identity.Subject, error) {
	if token == "" {
		return identity.Subject{}, ErrInvalid.New()
	}
	now := s.now().UTC()
	var r row
	err := s.live(s.db.NewSelect().Model(&r), now).Where("token_hash = ?", hash(token)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return identity.Subject{}, ErrInvalid.New()
	}
	if err != nil {
		return identity.Subject{}, errors.Wrap(err, "apitoken: validate")
	}
	if now.Sub(r.LastUsedAt) >= touchInterval {
		if err := s.touch(ctx, &r, now); err != nil {
			return identity.Subject{}, err
		}
	}
	sub := identity.Subject{
		Type:         identity.SubjectType(r.SubjectType),
		ID:           r.SubjectID,
		Method:       "bearer",
		Credential:   r.ID.String(),
		Tenant:       r.Tenant,
		Impersonator: r.impersonator(),
	}
	if r.Kind == KindPersonal {
		sub.Scopes = strings.Fields(r.Scopes)
	}
	return sub, nil
}

// touch 记录 token 在 now 被使用。条件更新保证并发请求中只有一个真正写入。
func (s *Store) touch(ctx context.Context, r *row, now time.Time) error {
	q := s.db.NewUpdate().Model((*row)(nil)).Set("last_used_at = ?", now).
		Where("id = ?", r.ID).Where("last_used_at < ?", now.Add(-touchInterval))
	if r.Kind == KindLogin {
		q = q.Set("expires_at = ?", s.cfg.deadline(r.CreatedAt, now, r.Remember))
	}
	_, err := q.Exec(ctx)
	return errors.Wrap(err, "apitoken: touch")
}

// List 返回 sub 未过期的 token，最近使用的在前；本次请求所用的 token（sub.Credential）标为 Current。
func (s *Store) List(ctx context.Context, sub identity.Subject) ([]Token, error) {
	var rows []row
	err := s.live(s.db.NewSelect().Model(&rows), s.now().UTC()).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).OrderExpr("last_used_at DESC").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "apitoken: list")
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		out = append(out, Token{
			ID:           r.ID.String(),
			Kind:         r.Kind,
			Name:         r.Name,
			Scopes:       strings.Fields(r.Scopes),
			Tenant:       r.Tenant,
			Impersonator: r.impersonator(),
			IP:           r.IP,
			UserAgent:    r.UserAgent,
			CreatedAt:    r.CreatedAt,
			LastUsedAt:   r.LastUsedAt,
			ExpiresAt:    r.ExpiresAt,
			Current:      r.ID.String() == sub.Credential,
		})
	}
	return out, nil
}

// Revoke 撤销 sub 的 token id；传入 sub.Credential 即登出当前设备。
// token 不存在或不属于 sub 时返回 ErrNotFound。
func (s *Store) Revoke(ctx context.Context, sub identity.Subject, id string) error {
	tid, err := uuid.Parse(id)
	if err != nil || sub.ID == "" {
		return ErrNotFound.New()
	}
	n, err := affected(s.db.NewDelete().Model((*row)(nil)).
		Where("id = ?", tid).Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).Exec(ctx))
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound.New()
	}
	return nil
}

// RevokeOthers 撤销 sub 除本次请求所用 token（sub.Credential）以外的全部登录 token，返回撤销的个数，
// 例如“退出其他设备”。个人访问令牌不受影响。sub 来自会话等其他凭据时，撤销它的全部登录 token。
func (s *Store) RevokeOthers(ctx context.Context, sub identity.Subject) (int, error) {
	if sub.ID == "" {
		return 0, errors.New("apitoken: subject ID is required")
	}
	q := s.db.NewDelete().Model((*row)(nil)).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).Where("kind = ?", KindLogin)
	if current, err := uuid.Parse(sub.Credential); err == nil {
		q = q.Where("id <> ?", current)
	}
	return affected(q.Exec(ctx))
}

// RevokeSubject 撤销主体 ref 的全部 token，包括个人访问令牌，返回撤销的个数。用于封禁账号、修改密码等。
func (s *Store) RevokeSubject(ctx context.Context, ref identity.Ref) (int, error) {
	if err := ref.Validate(); err != nil {
		return 0, errors.Wrap(err, "apitoken")
	}
	return affected(s.db.NewDelete().Model((*row)(nil)).
		Where("subject_type = ? AND subject_id = ?", ref.Type, ref.ID).Exec(ctx))
}

// Cleanup 删除最多 limit 个已过期的 token，返回删除的个数。由应用定时调用。
func (s *Store) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("apitoken: cleanup limit must be positive")
	}
	// 外层派生表让带 LIMIT 的子查询在 MySQL 中也能用于 IN。
	expired := s.db.NewSelect().Model((*row)(nil)).Column("id").
		Where("expires_at <= ?", s.now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	return affected(s.db.NewDelete().Model((*row)(nil)).
		Where("id IN (SELECT id FROM (?) AS expired)", expired).Exec(ctx))
}

func (r *row) impersonator() identity.Ref {
	return identity.Ref{Type: identity.SubjectType(r.ImpersonatorType), ID: r.ImpersonatorID}
}

// live 把查询限定在未过期的 token。
func (s *Store) live(q *bun.SelectQuery, now time.Time) *bun.SelectQuery {
	return q.Where("(expires_at IS NULL OR expires_at > ?)", now)
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// joinScopes 把 scope 存为空格分隔的字符串；scope 不能为空，也不能包含空白。
func joinScopes(scopes []string) (string, error) {
	for _, sc := range scopes {
		if sc == "" || strings.ContainsFunc(sc, unicode.IsSpace) {
			return "", errors.Newf("apitoken: invalid scope %q", sc)
		}
	}
	return strings.Join(scopes, " "), nil
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func affected(res sql.Result, err error) (int, error) {
	if err != nil {
		return 0, errors.Wrap(err, "apitoken: delete")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "apitoken: delete")
}
