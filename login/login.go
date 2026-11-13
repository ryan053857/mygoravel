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
	// ErrWeakPassword 表示密码不符合 Policy.PasswordPolicy。
	ErrWeakPassword = errorx.Define(errorx.InvalidArgument, "login.weak_password")
	// ErrTaken 表示登录标识已属于另一个主体。
	ErrTaken = errorx.Define(errorx.Conflict, "login.identifier_taken")
	// ErrLastMethod 表示移除这个登录标识或密码后，主体将没有可用的登录方式，见 Realm.Detach、Realm.RemovePassword。
	ErrLastMethod = errorx.Define(errorx.Conflict, "login.last_method")
	// ErrNotFound 表示登录标识不存在。
	ErrNotFound = errorx.Define(errorx.NotFound, "login.not_found")
	// ErrTwoFactorEnabled 表示已经启用了二次验证。
	ErrTwoFactorEnabled = errorx.Define(errorx.Conflict, "login.two_factor_enabled")
	// ErrMethodNotAllowed 表示登录策略（Policy.Methods）不允许这种登录方式，例如租户只允许用企业 IdP 登录。
	// 公开参数 method 是这种方式。
	ErrMethodNotAllowed = errorx.Define(errorx.PermissionDenied, "login.method_not_allowed")
	// ErrTwoFactorDisabled 表示没有启用二次验证。
	ErrTwoFactorDisabled = errorx.Define(errorx.Conflict, "login.two_factor_disabled")
)

// Options 是一套账号体系的登录配置。Type、Hasher、Crypt、Attempts、Sends 必填，其余有默认值。
type Options struct {
	// Type 是这套账号体系，Realm 只管理这个类型的主体。
	Type identity.SubjectType
	// PerTenant 为 true 时，登录标识在租户内唯一：查找和写入都使用 identity.TenantFromContext(ctx)，
	// 登录得到的主体带有这个 Tenant，签发的 token 只能在这个租户中使用。
	// 为 false 时登录标识全平台唯一，主体的 Tenant 为空。
	PerTenant bool
	// Hasher 哈希和校验密码。
	Hasher *hashing.Hasher
	// Crypt 派生验证码的 HMAC 密钥，验证码随应用密钥轮换。
	Crypt *crypt.Crypt
	// Send 发送验证码，例如发邮件或短信，内容和通道由应用决定。调用 SendCode 时必须设置。
	Send func(ctx context.Context, c Code) error
	// Issuer 是 TOTP 验证器中显示的发行者，通常是应用的名称，见 SetupTOTP。
	Issuer string
	// Policy 是登录策略：是否开放注册、密码规则、是否强制二次验证、允许哪些登录方式。设置了 PolicyFor 时不使用。
	Policy Policy
	// PolicyFor 按 ctx 返回登录策略，用于各租户不同的策略，例如读取租户的设置（settings.Group）。
	// PerTenant 时 ctx 中带有登录的租户（identity.TenantFromContext）。返回的错误原样交给调用方。为 nil 时使用 Policy。
	PolicyFor func(ctx context.Context) (Policy, error)
	// Resolve 在登录成功、签发凭据之前调用一次，通常与入口的 identity.Accounts 中这套账号的 Resolver 相同：
	// 返回错误（例如账号已停用）时登录失败，返回的 Permissions 等字段带入 Outcome.Subject。
	// Type、ID、Tenant 不能被它修改。为 nil 时不调用。
	Resolve identity.Resolver
	// CodeLength 是验证码的位数（数字），默认 6。
	CodeLength int
	// CodeTTL 是验证码的有效期，默认 15 分钟。
	CodeTTL time.Duration
	// CodeAttempts 是一个验证码最多可以尝试的次数，默认 3。
	CodeAttempts int
	// ProofTTL 是完成凭证的有效期，默认 15 分钟。
	ProofTTL time.Duration
	// Attempts 限制同一个标识的密码登录失败次数：每次尝试前消耗一次，成功后清零。
	// 多个实例共享配额时使用 redislimit，例如每 15 分钟 5 次。
	Attempts ratelimit.Limiter
	// Sends 限制向同一个标识发送验证码的次数，例如每 15 分钟 5 次。
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

// New 创建 Realm。db 必须已执行 login 的迁移。
// db 为 nil、Type 无效、Hasher、Crypt、Attempts 或 Sends 为 nil 时 panic。
func New(db bun.IDB, opts Options) *Realm {
	switch {
	case db == nil:
		panic("login: nil database")
	case opts.Hasher == nil:
		panic("login: Options.Hasher is required")
	case opts.Crypt == nil:
		panic("login: Options.Crypt is required")
	case opts.Attempts == nil || opts.Sends == nil:
		panic("login: Options.Attempts and Options.Sends are required")
	}
	if err := opts.Type.Validate(); err != nil {
		panic(errors.Wrap(err, "login: Options.Type"))
	}
	opts.CodeLength = cmp(opts.CodeLength, 6)
	opts.CodeTTL = cmp(opts.CodeTTL, 15*time.Minute)
	opts.CodeAttempts = cmp(opts.CodeAttempts, 3)
	opts.ProofTTL = cmp(opts.ProofTTL, 15*time.Minute)
	if opts.CodeLength < 4 || opts.CodeLength > 12 || opts.CodeTTL < 0 || opts.CodeAttempts < 0 || opts.ProofTTL < 0 {
		panic("login: CodeLength must be 4 to 12, and durations and attempts must not be negative")
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

// Policy 是一套账号体系的登录策略，见 Options.Policy 和 Options.PolicyFor。零值表示：不开放注册，
// email、phone、username 都可以与密码一起登录，默认的密码规则，不强制二次验证，不限制登录方式。
type Policy struct {
	// Register 为 true 时开放自助注册：向未注册的标识发送 SignIn 验证码，验证后得到注册用的完成凭证。
	// 为 false 时未注册的标识与找回密码一样不发送验证码，也不暴露它未注册。
	Register bool
	// PasswordKinds 是可以与密码一起登录的标识类型，默认 email、phone、username。
	PasswordKinds []string
	// PasswordPolicy 检查新密码，不符合时返回错误（通常是 ErrWeakPassword）。
	// 默认要求 8 到 128 个字符，并且首尾没有空白。已设置的密码不受之后修改的策略影响。
	// Hasher 使用 bcrypt 时，超过 72 字节的密码同样返回 ErrWeakPassword。
	PasswordPolicy func(password string) error
	// RequireTwoFactor 为 true 时必须使用二次验证，例如运营人员，或者要求成员使用二次验证的租户。
	// 这次登录没有经过二次验证（passkey 登录、External.TwoFactor 本身就算）时不签发凭据：账号已经启用二次验证的，
	// 返回 Outcome.Pending，第三方登录也是如此；还没有启用的，返回 Outcome.Enroll，先启用再登录，见 EnrollTOTP。
	RequireTwoFactor bool
	// Methods 是允许的登录方式，名称与 identity.Auth.Methods 相同：password、email、phone、第三方提供方的名称、passkey。
	// nil 表示不限制；例如租户要求只用自己的企业 IdP 登录时，只列出这个提供方。不允许的方式返回 ErrMethodNotAllowed：
	// Password、SendCode（SignIn）、VerifyCode（登录）和 External 都检查它。二次验证的第二步不受限制。
	Methods []string
}

// policy 返回 ctx 的登录策略，补上默认值。
func (r *Realm) policy(ctx context.Context) (Policy, error) {
	p := r.opts.Policy
	if r.opts.PolicyFor != nil {
		var err error
		if p, err = r.opts.PolicyFor(ctx); err != nil {
			return Policy{}, err
		}
	}
	if p.PasswordKinds == nil {
		p.PasswordKinds = []string{KindEmail, KindPhone, KindUsername}
	}
	if p.PasswordPolicy == nil {
		p.PasswordPolicy = defaultPolicy
	}
	return p, nil
}

// allow 确认 ctx 的登录策略允许 method，不允许时返回 ErrMethodNotAllowed。
func (r *Realm) allow(ctx context.Context, method string) (Policy, error) {
	p, err := r.policy(ctx)
	if err != nil {
		return Policy{}, err
	}
	if p.Methods != nil && !slices.Contains(p.Methods, method) {
		return Policy{}, ErrMethodNotAllowed.New(errorx.Public("method", method))
	}
	return p, nil
}

// Outcome 是一次身份证明的结果，四者只有一个不为零值。
type Outcome struct {
	// Subject 是登录成功的主体：Type、ID，PerTenant 时还有 Tenant，Auth 记下这次通过的验证方式（见 MethodPassword），
	// 以及 Options.Resolve 加载的字段。交给 tokens.Login 或 JWT.Login 签发凭据。
	Subject identity.Subject
	// Pending 表示第一步验证已通过，但账号启用了二次验证：把它交给客户端，
	// 客户端连同 TOTP 验证码或恢复码提交给 TwoFactor 完成登录。
	Pending string
	// Proof 是需要应用继续完成的一次性完成凭证：标识未注册时交给 Register，找回密码时交给 ResetPassword，
	// 绑定标识时交给 Link。
	Proof string
	// Enroll 表示身份已经得到证明，但 Policy.RequireTwoFactor 要求二次验证而账号还没有启用：把它交给客户端，
	// 客户端用 EnrollTOTP 取得二维码，再用 CompleteEnroll 提交验证器上的数字，启用后登录成功。
	Enroll string
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

// 登录方式的名称，记录在 identity.Auth.Methods 中。验证码登录记为标识的类型（email、phone），
// 第三方登录记为提供方的名称（如 google），passkey 登录记为 passkey。
const (
	MethodPassword = "password" // 密码
	MethodTOTP     = "totp"     // 验证器 App 上的数字
	MethodRecovery = "recovery" // 恢复码
	MethodIdPMFA   = "idp_mfa"  // 第三方提供方做过多因素认证，见 External.TwoFactor
)

// signedIn 返回登录成功的结果，主体的 Auth 记下这次通过的验证方式；签发前调用 Options.Resolve。
func (r *Realm) signedIn(ctx context.Context, id, tenant string, methods ...string) (Outcome, error) {
	sub := identity.Subject{Type: r.opts.Type, ID: id, Tenant: tenant, Auth: identity.Auth{Methods: methods, Time: r.now().UTC()}}
	if resolve := r.opts.Resolve; resolve != nil {
		resolved, err := resolve(ctx, sub)
		if err != nil {
			return Outcome{}, err
		}
		resolved.Type, resolved.ID, resolved.Tenant, resolved.Auth = sub.Type, sub.ID, sub.Tenant, sub.Auth
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
