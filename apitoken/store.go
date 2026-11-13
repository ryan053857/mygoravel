package apitoken

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"slices"
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
	// KindRefresh 是 JWT 模式的登录（见 JWT），同样按 Config 滑动过期、代表一台设备；
	// 客户端持有的是 refresh token，每次刷新时轮换，请求使用短期的 access token。
	KindRefresh Kind = "refresh"
	// KindPersonal 是个人访问令牌，供脚本和第三方集成使用，过期时间由创建者指定。
	KindPersonal Kind = "personal"
	// KindOAuth 是用户授权 OAuth 客户端（例如 claude.ai 的连接器）后签发的令牌，见 Authorize。
	// 它只能用于授权时指定的资源，由 ValidateFor 校验；Validate 不接受它。
	KindOAuth Kind = "oauth"
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
	Scopes       []string     // 个人访问令牌和 OAuth 令牌的权限限制；登录 token 为空
	ClientID     string       // OAuth 令牌授权的客户端；其余为空
	ClientName   string       // OAuth 令牌授权时客户端的名称，例如 Claude；其余为空
	Resource     string       // OAuth 令牌只能用于的资源，例如 https://blog.example.com/mcp；其余为空
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

// Store 是基于 Bun 的 token 存储，Migrations 提供表结构。可以并发使用。
//
// Config 只在 Login 签发时使用：登录 token 的空闲时长和最长期限写入 token 本身，Validate、List、Revoke 等不读取它。
// 所以多个 Store 可以共用一张表，例如每套账号体系用自己的 Config 创建一个 Store 签发 token，
// 校验交给其中任何一个都得到相同的结果。
//
// Store 不在后台清理过期 token：过期 token 校验不通过，由应用定时调用 Cleanup 删除。
type Store struct {
	db  bun.IDB
	cfg Config
}

type row struct {
	bun.BaseModel `bun:"table:api_tokens,alias:t"`

	ID               uuid.UUID `bun:"id,pk,type:uuid"`
	TokenHash        string    `bun:"token_hash,notnull"`
	PreviousHash     string    `bun:"previous_hash,nullzero"` // refresh 轮换前的上一个 token
	RotatedAt        time.Time `bun:"rotated_at,nullzero"`    // refresh 最近一次轮换的时间
	SubjectType      string    `bun:"subject_type,notnull"`
	SubjectID        string    `bun:"subject_id,notnull"`
	Tenant           string    `bun:"tenant,notnull"`
	ImpersonatorType string    `bun:"impersonator_type,notnull"`
	ImpersonatorID   string    `bun:"impersonator_id,notnull"`
	Kind             Kind      `bun:"kind,notnull"`
	Name             string    `bun:"name,notnull"`
	Scopes           string    `bun:"scopes,notnull"`
	IdleTimeout      int64     `bun:"idle_timeout,notnull"` // 秒；0 表示不按空闲延长
	EndsAt           time.Time `bun:"ends_at,nullzero"`     // 登录 token 的最长期限，滑动延长不超过它
	IP               string    `bun:"ip,notnull"`
	UserAgent        string    `bun:"user_agent,notnull"`
	CreatedAt        time.Time `bun:"created_at,notnull"`
	LastUsedAt       time.Time `bun:"last_used_at,notnull"`
	ExpiresAt        time.Time `bun:"expires_at,nullzero"`
	ClientID         string    `bun:"client_id,notnull"`
	ClientName       string    `bun:"client_name,notnull"`
	Resource         string    `bun:"resource,notnull"`
	AuthMethods      string    `bun:"auth_methods,notnull"` // 登录的验证方式，空格分隔；其他种类为空
	AuthTime         time.Time `bun:"auth_time,nullzero"`   // 登录最近一次验证身份的时间
}

// NewStore 创建 token 存储。db 必须已执行 api_tokens 迁移。cfg 不合法或 db 为 nil 时 panic。
func NewStore(db bun.IDB, cfg Config) *Store {
	if db == nil {
		panic("apitoken: nil database")
	}
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "apitoken"))
	}
	return &Store{db: db, cfg: cfg}
}

// Login 为 sub 签发登录 token，返回 token 明文，明文只在这里返回一次。
// 有效期按此刻的 Config 确定并写入 token：最长 Lifetime，空闲时长为 IdleTimeout（Device.Remember 时为 RememberIdleTimeout）。
//
// 登录 token 代表用户本人，不限制权限：校验得到的主体 Scopes 为 nil，权限由 Resolve 按用户当前的角色加载。
// sub.Tenant 不为空时，token 只能在这个租户中使用，例如只属于某个租户的终端客户账号。
// sub.Impersonator 不为零值时签发的是代为登录的 token，校验得到的主体带有同样的 Impersonator。
// sub.Auth 记录这次登录怎样验证了身份（login.Realm 填写），校验得到的主体带有它，见 Confirm。
func (s *Store) Login(ctx context.Context, sub identity.Subject, d Device) (string, error) {
	plain, _, err := s.login(ctx, sub, d, KindLogin)
	return plain, err
}

// login 签发 kind 类的登录 token，返回明文和写入的行。
func (s *Store) login(ctx context.Context, sub identity.Subject, d Device, kind Kind) (string, *row, error) {
	now := time.Now().UTC()
	methods, err := joinWords(sub.Auth.Methods, "auth method")
	if err != nil {
		return "", nil, err
	}
	idle, end := s.cfg.idle(d.Remember), now.Add(s.cfg.Lifetime)
	r := &row{
		AuthMethods: methods,
		AuthTime:    sub.Auth.Time.UTC(),
		Kind:        kind,
		Tenant:      sub.Tenant,
		IdleTimeout: int64(idle / time.Second),
		EndsAt:      end,
		IP:          d.IP,
		UserAgent:   truncate(d.UserAgent, maxUserAgent),
		ExpiresAt:   deadline(end, idle, now),
	}
	plain, err := s.issue(ctx, sub, r, now)
	return plain, r, err
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
	now := time.Now().UTC()
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

// Grant 描述用户对一个 OAuth 客户端的授权，由 OAuth 授权服务器（oauthserver）在用户同意后使用。
type Grant struct {
	// ClientID 是客户端 ID，ClientName 是同意页上显示的名称，记录下来用于“已连接的应用”列表。
	ClientID   string
	ClientName string
	// Resource 是令牌只能用于的资源，例如 MCP 服务的地址 https://blog.example.com/mcp。
	Resource string
	// Scopes 是用户同意的范围，至少一个，与个人访问令牌的 Scopes 相同：令牌能做的是用户当前权限与它的交集。
	Scopes []string
	// Lifetime 是令牌从签发起的最长有效期，IdleTimeout 是最长空闲时间（0 表示不限制），与登录 token 相同按使用滑动延长。
	Lifetime    time.Duration
	IdleTimeout time.Duration
}

// Authorize 为 sub 签发 OAuth 令牌，返回 token 明文，明文只在这里返回一次。sub.Tenant 不为空时令牌只能在这个租户中使用。
// 令牌只由 ValidateFor(g.Resource) 接受；它出现在 List 中，可以用 Revoke 撤销，不受 RevokeOthers 影响。
func (s *Store) Authorize(ctx context.Context, sub identity.Subject, g Grant) (string, error) {
	if g.ClientID == "" || g.Resource == "" {
		return "", errors.New("apitoken: an OAuth grant needs a client ID and a resource")
	}
	if len(g.Scopes) == 0 {
		return "", errors.New("apitoken: an OAuth grant needs at least one scope")
	}
	if g.Lifetime <= 0 || g.IdleTimeout < 0 {
		return "", errors.New("apitoken: an OAuth grant needs a positive lifetime and a non-negative idle timeout")
	}
	joined, err := joinScopes(g.Scopes)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	end := now.Add(g.Lifetime)
	r := &row{
		Kind:        KindOAuth,
		Name:        truncate(g.ClientName, maxName),
		Scopes:      joined,
		Tenant:      sub.Tenant,
		IdleTimeout: int64(g.IdleTimeout / time.Second),
		EndsAt:      end,
		ExpiresAt:   deadline(end, g.IdleTimeout, now),
		ClientID:    g.ClientID,
		ClientName:  truncate(g.ClientName, maxName),
		Resource:    g.Resource,
	}
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
	plain := secret()
	r.ID, r.TokenHash = id, hash(plain)
	r.SubjectType, r.SubjectID = string(sub.Type), sub.ID
	r.ImpersonatorType, r.ImpersonatorID = string(sub.Impersonator.Type), sub.Impersonator.ID
	r.CreatedAt, r.LastUsedAt = now, now
	if _, err := s.db.NewInsert().Model(r).Exec(ctx); err != nil {
		return "", errors.Wrap(err, "apitoken: insert")
	}
	return plain, nil
}

// Validate 实现 rest.TokenValidator，校验登录 token 和个人访问令牌：返回 token 代表的主体，Method 为 bearer，Credential 为 token 的 ID，
// Tenant 为 token 绑定的租户，Impersonator 为签发时的代为登录者。登录 token 的 Scopes 为 nil，个人访问令牌的 Scopes 是创建时的限制；
// 两者的 Permissions 都为空，由 Resolve 加载。token 不存在、已撤销或已过期时返回 ErrInvalid。
//
// 距上次记录使用超过一分钟时，更新最近使用时间，并延长登录 token 的有效期。
// JWT 模式的 refresh token 不能直接作为 Bearer 凭据，OAuth 令牌只能用于它的资源（见 ValidateFor），两者校验都返回 ErrInvalid。
func (s *Store) Validate(ctx context.Context, token string) (identity.Subject, error) {
	return s.validate(ctx, token, "")
}

// ValidateFor 返回只接受发给 resource 的令牌的校验器：登录 token、个人访问令牌，以及 Resource 为 resource 的 OAuth 令牌。
// 发给其他资源的 OAuth 令牌按无效处理。用于 OAuth 客户端访问的资源，例如 mcpx.HandlerOptions.Validate：
//
//	Validate: tokens.ValidateFor("https://blog.example.com/mcp")
//
// OAuth 令牌得到的主体与个人访问令牌相同，Scopes 是用户同意的范围。resource 为空时 panic。
func (s *Store) ValidateFor(resource string) func(ctx context.Context, token string) (identity.Subject, error) {
	if resource == "" {
		panic("apitoken: ValidateFor needs a resource")
	}
	return func(ctx context.Context, token string) (identity.Subject, error) {
		return s.validate(ctx, token, resource)
	}
}

// validate 校验 token。resource 为空时不接受 OAuth 令牌，否则只接受发给 resource 的 OAuth 令牌。
func (s *Store) validate(ctx context.Context, token, resource string) (identity.Subject, error) {
	if token == "" {
		return identity.Subject{}, ErrInvalid.New()
	}
	now := time.Now().UTC()
	var r row
	q := s.live(s.db.NewSelect().Model(&r), now).Where("token_hash = ?", hash(token)).Where("kind <> ?", KindRefresh)
	if resource == "" {
		q = q.Where("kind <> ?", KindOAuth)
	} else {
		q = q.Where("(kind <> ? OR resource = ?)", KindOAuth, resource)
	}
	err := q.Scan(ctx)
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
	if r.Kind == KindPersonal || r.Kind == KindOAuth {
		sub.Scopes = strings.Fields(r.Scopes)
	}
	if r.Kind == KindLogin {
		sub.Auth = r.auth()
	}
	return sub, nil
}

// Confirm 记录 sub 本次请求所用的登录（sub.Credential）此刻再次验证了身份，例如用户重新输入了密码（login.Realm.Confirm），
// methods 是这次用到的验证方式，补充到登录的 Auth.Methods 中；之后 rest.Confirmed 的路由在有效期内放行。
// 用二次验证确认后，没有经过二次验证的登录也变为经过了（identity.Auth.TwoFactor），不需要重新登录。
//
// 不透明的登录 token 下一个请求就生效；JWT 模式用 JWT.Confirm，它同时返回带有新验证情况的 access token。
// sub.Credential 不是 sub 的登录（个人访问令牌、OAuth 令牌）时返回 ErrNotFound；代为登录的 token 返回 identity.ErrForbidden，
// 实际操作的人不是主体本人。methods 不能为空。
func (s *Store) Confirm(ctx context.Context, sub identity.Subject, methods ...string) error {
	_, err := s.confirm(ctx, sub, methods, KindLogin, KindRefresh)
	return err
}

// confirm 更新 kinds 类登录的验证情况，返回更新后的行。
func (s *Store) confirm(ctx context.Context, sub identity.Subject, methods []string, kinds ...Kind) (*row, error) {
	if len(methods) == 0 {
		return nil, errors.New("apitoken: Confirm needs at least one auth method")
	}
	if _, err := joinWords(methods, "auth method"); err != nil {
		return nil, err
	}
	if !sub.Impersonator.IsZero() {
		return nil, identity.ErrForbidden.New()
	}
	id, err := uuid.Parse(sub.Credential)
	if err != nil || sub.ID == "" {
		return nil, ErrNotFound.New()
	}
	now := time.Now().UTC()
	var r row
	err = s.live(s.db.NewSelect().Model(&r), now).Where("id = ?", id).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).
		Where("kind IN (?)", bun.List(kinds)).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New()
	}
	if err != nil {
		return nil, errors.Wrap(err, "apitoken: confirm")
	}
	merged := strings.Fields(r.AuthMethods)
	for _, m := range methods {
		if !slices.Contains(merged, m) {
			merged = append(merged, m)
		}
	}
	r.AuthMethods, r.AuthTime = strings.Join(merged, " "), now
	_, err = s.db.NewUpdate().Model((*row)(nil)).
		Set("auth_methods = ?", r.AuthMethods).Set("auth_time = ?", r.AuthTime).Where("id = ?", r.ID).Exec(ctx)
	return &r, errors.Wrap(err, "apitoken: confirm")
}

// touch 记录 token 在 now 被使用。条件更新保证并发请求中只有一个真正写入。
func (s *Store) touch(ctx context.Context, r *row, now time.Time) error {
	q := s.db.NewUpdate().Model((*row)(nil)).Set("last_used_at = ?", now).
		Where("id = ?", r.ID).Where("last_used_at < ?", now.Add(-touchInterval))
	if r.IdleTimeout > 0 {
		q = q.Set("expires_at = ?", deadline(r.EndsAt, time.Duration(r.IdleTimeout)*time.Second, now))
	}
	_, err := q.Exec(ctx)
	return errors.Wrap(err, "apitoken: touch")
}

// List 返回 sub 未过期的 token，最近使用的在前；本次请求所用的 token（sub.Credential）标为 Current。
func (s *Store) List(ctx context.Context, sub identity.Subject) ([]Token, error) {
	var rows []row
	err := s.live(s.db.NewSelect().Model(&rows), time.Now().UTC()).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).OrderExpr("last_used_at DESC").Scan(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "apitoken: list")
	}
	out := make([]Token, 0, len(rows))
	for _, r := range rows {
		t := r.token()
		t.Current = t.ID == sub.Credential
		out = append(out, t)
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

// RevokeToken 按明文撤销一个 OAuth 令牌，用于 OAuth 的撤销端点（RFC 7009）：客户端断开连接时交回令牌。
// 令牌不存在、已过期或不是 OAuth 令牌时什么也不做，返回 nil。
func (s *Store) RevokeToken(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	_, err := affected(s.db.NewDelete().Model((*row)(nil)).
		Where("token_hash = ?", hash(token)).Where("kind = ?", KindOAuth).Exec(ctx))
	return err
}

// RevokeOthers 撤销 sub 除本次请求所用 token（sub.Credential）以外的全部登录 token（包括 JWT 模式的登录），返回撤销的个数，
// 例如“退出其他设备”。个人访问令牌不受影响。sub.Credential 不是 sub 的 token 时，撤销它的全部登录 token。
func (s *Store) RevokeOthers(ctx context.Context, sub identity.Subject) (int, error) {
	if sub.ID == "" {
		return 0, errors.New("apitoken: subject ID is required")
	}
	q := s.db.NewDelete().Model((*row)(nil)).
		Where("subject_type = ? AND subject_id = ?", sub.Type, sub.ID).Where("kind IN (?)", bun.List([]Kind{KindLogin, KindRefresh}))
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
	expired := s.db.NewSelect().Model((*row)(nil)).Column("id").
		Where("expires_at <= ?", time.Now().UTC()).OrderExpr("expires_at").Limit(int64(limit))
	return affected(s.db.NewDelete().Model((*row)(nil)).
		Where("id IN (?)", expired).Exec(ctx))
}

func (r *row) token() Token {
	return Token{
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
		ClientID:     r.ClientID,
		ClientName:   r.ClientName,
		Resource:     r.Resource,
	}
}

// auth 返回登录的验证情况。
func (r *row) auth() identity.Auth {
	return identity.Auth{Methods: strings.Fields(r.AuthMethods), Time: r.AuthTime}
}

func (r *row) impersonator() identity.Ref {
	return identity.Ref{Type: identity.SubjectType(r.ImpersonatorType), ID: r.ImpersonatorID}
}

// live 把查询限定在未过期的 token。
func (s *Store) live(q *bun.SelectQuery, now time.Time) *bun.SelectQuery {
	return q.Where("(expires_at IS NULL OR expires_at > ?)", now)
}

// secret 生成 token 明文：32 字节随机数的 URL 安全 base64。
func secret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// joinScopes 把 scope 存为空格分隔的字符串；scope 不能为空，也不能包含空白。
func joinScopes(scopes []string) (string, error) { return joinWords(scopes, "scope") }

// joinWords 把 words 存为空格分隔的字符串；每一项不能为空，也不能包含空白。what 用于错误信息。
func joinWords(words []string, what string) (string, error) {
	for _, w := range words {
		if w == "" || strings.ContainsFunc(w, unicode.IsSpace) {
			return "", errors.Newf("apitoken: invalid %s %q", what, w)
		}
	}
	return strings.Join(words, " "), nil
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
