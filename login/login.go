package login

import (
	"context"
	"database/sql"
	"slices"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/crypt"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/hashing"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/ratelimit"
)

var (
	// ErrInvalidCredentials 表示登录标识或密码不正确。两种情况不作区分，防止探测哪些标识已注册。
	ErrInvalidCredentials = errorx.Define(errorx.Unauthenticated, "login.invalid_credentials")
	// ErrInvalidCode 表示验证码不正确、已过期、已用过或尝试次数已用完。
	ErrInvalidCode = errorx.Define(errorx.InvalidArgument, "login.invalid_code")
	// ErrInvalidProof 表示完成凭证不正确、已过期或已用过。
	ErrInvalidProof = errorx.Define(errorx.InvalidArgument, "login.invalid_proof")
	// ErrInvalidIdentifier 表示登录标识的格式不正确，例如手机号不是 E.164。
	ErrInvalidIdentifier = errorx.Define(errorx.InvalidArgument, "login.invalid_identifier")
	// ErrWeakPassword 表示密码不符合 Options.PasswordPolicy。
	ErrWeakPassword = errorx.Define(errorx.InvalidArgument, "login.weak_password")
	// ErrTaken 表示登录标识已属于另一个主体。
	ErrTaken = errorx.Define(errorx.Conflict, "login.identifier_taken")
	// ErrLastIdentifier 表示要移除的是主体最后一个登录标识，移除后将无法登录。
	ErrLastIdentifier = errorx.Define(errorx.Conflict, "login.last_identifier")
	// ErrNotFound 表示登录标识不存在。
	ErrNotFound = errorx.Define(errorx.NotFound, "login.not_found")
	// ErrTwoFactorEnabled 表示已经启用了二次验证。
	ErrTwoFactorEnabled = errorx.Define(errorx.Conflict, "login.two_factor_enabled")
	// ErrTwoFactorDisabled 表示没有启用二次验证。
	ErrTwoFactorDisabled = errorx.Define(errorx.Conflict, "login.two_factor_disabled")
)

// Options 是一套账号体系的登录配置。Type、Hasher、Crypt 必填，其余有默认值。
type Options struct {
	// Type 是这套账号体系，Realm 只管理这个类型的主体。
	Type identity.SubjectType
	// PerTenant 为 true 时，登录标识在租户内唯一：查找和写入都使用 identity.TenantFromContext(ctx)，
	// 登录得到的主体带有这个 Tenant，签发的会话或 token 只能在这个租户中使用。
	// 为 false 时登录标识全平台唯一，主体的 Tenant 为空。
	PerTenant bool
	// Register 为 true 时开放自助注册：向未注册的标识发送 SignIn 验证码，验证后得到注册用的完成凭证。
	// 为 false 时未注册的标识与找回密码一样不发送验证码，也不暴露它未注册。
	Register bool
	// Hasher 哈希和校验密码。
	Hasher *hashing.Hasher
	// Crypt 派生验证码的 HMAC 密钥，验证码随应用密钥轮换。
	Crypt *crypt.Crypt
	// Send 发送验证码，例如发邮件或短信，内容和通道由应用决定。调用 SendCode 时必须设置。
	Send func(ctx context.Context, c Code) error
	// Issuer 是 TOTP 验证器中显示的发行者，通常是应用的名称，见 SetupTOTP。
	Issuer string
	// Resolve 在登录成功、签发凭据之前调用一次，通常与入口的 identity.Accounts 中这套账号的 Resolver 相同：
	// 返回错误（例如账号已停用）时登录失败，返回的 Permissions 等字段带入 Outcome.Subject。
	// Type、ID、Tenant 不能被它修改。为 nil 时不调用。
	Resolve identity.Resolver
	// PasswordKinds 是可以与密码一起登录的标识类型，默认 email、phone、username。
	PasswordKinds []string
	// PasswordPolicy 检查新密码，不符合时返回错误（通常是 ErrWeakPassword）。
	// 默认要求 8 到 128 个字符，并且首尾没有空白。已设置的密码不受之后修改的策略影响。
	// Hasher 使用 bcrypt 时，超过 72 字节的密码同样返回 ErrWeakPassword。
	PasswordPolicy func(password string) error
	// CodeLength 是验证码的位数（数字），默认 6。
	CodeLength int
	// CodeTTL 是验证码的有效期，默认 15 分钟。
	CodeTTL time.Duration
	// CodeAttempts 是一个验证码最多可以尝试的次数，默认 3。
	CodeAttempts int
	// ProofTTL 是完成凭证的有效期，默认 15 分钟。
	ProofTTL time.Duration
	// Attempts 限制同一个标识的密码登录失败次数：每次尝试前消耗一次，成功后清零。
	// 默认是数据库限流器，每 15 分钟 5 次，需要 ratelimit 的迁移。
	Attempts ratelimit.Limiter
	// Sends 限制向同一个标识发送验证码的次数，默认是数据库限流器，每 15 分钟 5 次。
	Sends ratelimit.Limiter
}

// Realm 管理一套账号体系的登录凭据：登录标识、密码、验证码与完成凭证，数据保存在框架的 auth_* 表中，
// 按 identity.Ref 引用主体。账号本身（例如 staff、customers 表）由应用管理。可以并发使用。
type Realm struct {
	db           bun.IDB
	opts         Options
	codeKeys     [][]byte
	recoveryKeys [][]byte
	dummy        func() string // 标识不存在时用于校验的密码哈希，使耗时与存在时一致
	now          func() time.Time
}

// New 创建 Realm。db 必须已执行 login 的迁移；使用默认限流器时还需要 ratelimit 的迁移。
// db 为 nil、Type 无效、Hasher 或 Crypt 为 nil 时 panic。
func New(db bun.IDB, opts Options) *Realm {
	switch {
	case db == nil:
		panic("login: nil database")
	case opts.Hasher == nil:
		panic("login: Options.Hasher is required")
	case opts.Crypt == nil:
		panic("login: Options.Crypt is required")
	}
	if err := opts.Type.Validate(); err != nil {
		panic(errors.Wrap(err, "login: Options.Type"))
	}
	if opts.PasswordKinds == nil {
		opts.PasswordKinds = []string{KindEmail, KindPhone, KindUsername}
	}
	if opts.PasswordPolicy == nil {
		opts.PasswordPolicy = defaultPolicy
	}
	opts.CodeLength = cmp(opts.CodeLength, 6)
	opts.CodeTTL = cmp(opts.CodeTTL, 15*time.Minute)
	opts.CodeAttempts = cmp(opts.CodeAttempts, 3)
	opts.ProofTTL = cmp(opts.ProofTTL, 15*time.Minute)
	if opts.CodeLength < 4 || opts.CodeLength > 12 || opts.CodeTTL < 0 || opts.CodeAttempts < 0 || opts.ProofTTL < 0 {
		panic("login: CodeLength must be 4 to 12, and durations and attempts must not be negative")
	}
	every15 := ratelimit.Config{Rate: 5, Interval: 15 * time.Minute, Burst: 5}
	if opts.Attempts == nil {
		opts.Attempts = ratelimit.NewDatabase(db, "login:"+string(opts.Type)+":attempts", every15)
	}
	if opts.Sends == nil {
		opts.Sends = ratelimit.NewDatabase(db, "login:"+string(opts.Type)+":sends", every15)
	}
	hasher := opts.Hasher
	return &Realm{
		db:           db,
		opts:         opts,
		codeKeys:     opts.Crypt.Derive("mygoravel login code v1"),
		recoveryKeys: opts.Crypt.Derive("mygoravel login recovery v1"),
		dummy: sync.OnceValue(func() string {
			h, _ := hasher.Make("mygoravel login dummy password")
			return h
		}),
		now: time.Now,
	}
}

func cmp[T int | time.Duration](v, def T) T {
	if v == 0 {
		return def
	}
	return v
}

// defaultPolicy 要求 8 到 128 个字符，首尾没有空白。
func defaultPolicy(password string) error {
	n := len([]rune(password))
	if n < 8 || n > 128 || password != trimSpace(password) {
		return ErrWeakPassword.New()
	}
	return nil
}

// Outcome 是一次身份证明的结果，三者只有一个不为零值。
type Outcome struct {
	// Subject 是登录成功的主体：Type、ID，PerTenant 时还有 Tenant，以及 Options.Resolve 加载的字段。
	// 交给 pages.Login 或 tokens.Login 签发凭据。
	Subject identity.Subject
	// Pending 表示第一步验证已通过，但账号启用了二次验证：把它交给客户端，
	// 客户端连同 TOTP 验证码或恢复码提交给 TwoFactor 完成登录。
	Pending string
	// Proof 是需要应用继续完成的一次性完成凭证：标识未注册时交给 Register，找回密码时交给 ResetPassword，
	// 绑定标识时交给 Link。
	Proof string
}

// Type 返回这套账号体系，即 Options.Type。
func (r *Realm) Type() identity.SubjectType { return r.opts.Type }

// tenant 返回本次操作所在的租户：PerTenant 时取自 ctx，否则为空。
func (r *Realm) tenant(ctx context.Context) string {
	if r.opts.PerTenant {
		return identity.TenantFromContext(ctx)
	}
	return ""
}

// check 确认 ref 属于这套账号体系。
func (r *Realm) check(ref identity.Ref) error {
	if err := ref.Validate(); err != nil {
		return errors.Wrap(err, "login")
	}
	if ref.Type != r.opts.Type {
		return errors.Newf("login: subject type %q does not belong to realm %q", ref.Type, r.opts.Type)
	}
	return nil
}

// signedIn 返回登录成功的结果，签发前调用 Options.Resolve。
func (r *Realm) signedIn(ctx context.Context, id, tenant string) (Outcome, error) {
	sub := identity.Subject{Type: r.opts.Type, ID: id, Tenant: tenant}
	if resolve := r.opts.Resolve; resolve != nil {
		resolved, err := resolve(ctx, sub)
		if err != nil {
			return Outcome{}, err
		}
		resolved.Type, resolved.ID, resolved.Tenant = sub.Type, sub.ID, sub.Tenant
		sub = resolved
	}
	return Outcome{Subject: sub}, nil
}

// upsert 让 q 在主键冲突时改为更新 set 中的列。
func upsert(q *bun.InsertQuery, conflict string, set ...string) *bun.InsertQuery {
	q = q.On("CONFLICT (" + conflict + ") DO UPDATE")
	for _, c := range set {
		q = q.Set("? = EXCLUDED.?", bun.Ident(c), bun.Ident(c))
	}
	return q
}

// affected 返回语句影响的行数。
func affected(res sql.Result, err error) (int64, error) {
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func containsKind(kinds []string, kind string) bool { return slices.Contains(kinds, kind) }
