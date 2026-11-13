package identity

import (
	"context"
	"slices"
	"strings"
	"time"
)

// Subject 是已认证主体：Type 是它所属的账号体系，ID 是它在其中的标识，两者合起来见 Ref。
//
// Permissions 是主体在 Tenant 中拥有的权限，Scopes 是本次请求所用凭据被限制在内的权限，
// 两者共同决定 Can：例如用户拥有 articles:*，他创建的只读令牌的 Scopes 为 articles:read。
//
// Impersonator 不为零值表示代为登录：实际操作的人是 Impersonator，他以这个主体的身份和权限操作，例如客服代用户登录排查问题。
// Impersonator 由凭据记录，审计日志应同时记录主体（Ref）和 Impersonator。
type Subject struct {
	Type         SubjectType
	ID           string
	Impersonator Ref      // 代为登录时实际操作的人，可以属于另一套账号体系；零值表示主体本人
	Method       string   // 认证方式，如 bearer
	Credential   string   // 本次请求所用凭据的 ID，如 API token 的 ID；用于登出当前设备、在设备列表中标出当前设备，可为空
	Tenant       string   // 本次请求所在的租户，"" 表示平台本身；由凭据设置时，凭据只能在这个租户中使用
	Permissions  []string // 主体在 Tenant 中拥有的权限，支持通配符，见 Can
	Scopes       []string // 凭据的权限限制，支持通配符；nil 表示不限制，空切片表示什么都不允许
	Auth         Auth     // 凭据是怎么得到的：登录时通过的验证方式和最近一次验证身份的时间；不经过登录的凭据为零值
}

// Auth 记录一次登录怎样验证了主体的身份，由凭据保存（见 apitoken），对应 OIDC 的 amr 和 auth_time。
// 个人访问令牌、OAuth 令牌、代为登录等不经过本人登录的凭据为零值，过不了 rest.Confirmed 和二次验证的要求。
type Auth struct {
	// Methods 是这次登录依次通过的验证方式，之后确认身份时补充新的方式，例如：
	// password；email、phone（验证码发到哪里）；totp、recovery（二次验证）；passkey；第三方提供方的名称，如 google，
	// 提供方做过多因素认证时再加上 idp_mfa。
	Methods []string
	// Time 是最近一次验证身份的时间：登录，或之后的确认身份。
	Time time.Time
}

// 计入二次验证的方式：TOTP 和恢复码是第二步，passkey 本身同时验证了设备和使用者，idp_mfa 是第三方提供方做过的多因素认证。
var twoFactorMethods = []string{"totp", "recovery", "passkey", "idp_mfa"}

// TwoFactor 报告这次登录是否经过了二次验证：Methods 中有 totp、recovery、passkey 或 idp_mfa。
func (a Auth) TwoFactor() bool {
	return slices.ContainsFunc(a.Methods, func(m string) bool { return slices.Contains(twoFactorMethods, m) })
}

// ConfirmedWithin 报告最近一次验证身份是否在 d 之内。Time 为零值时返回 false。
func (a Auth) ConfirmedWithin(d time.Duration) bool {
	return !a.Time.IsZero() && time.Since(a.Time) <= d
}

// Ref 返回主体的引用。
func (s Subject) Ref() Ref { return Ref{Type: s.Type, ID: s.ID} }

// Can 报告主体能否执行 perm：Permissions 授予了它，并且 Scopes 为 nil 或也允许它。
//
// 权限是以冒号分隔的名称，例如 articles:write。授予时可以用 * 作为最后一段，
// 匹配以前面各段开头的全部权限：articles:* 匹配 articles:write 和 articles:comments:delete，
// 单独的 * 匹配全部权限。perm 本身不应包含通配符。
func (s Subject) Can(perm string) bool {
	return Grants(s.Permissions, perm) && (s.Scopes == nil || Grants(s.Scopes, perm))
}

// Grants 报告 granted 中是否有权限匹配 perm，通配符规则见 Subject.Can。
func Grants(granted []string, perm string) bool {
	if perm == "" {
		return false
	}
	for _, g := range granted {
		if g == perm || g == "*" {
			return true
		}
		if prefix, ok := strings.CutSuffix(g, ":*"); ok && strings.HasPrefix(perm, prefix+":") {
			return true
		}
	}
	return false
}

type ctxKey struct{}

type tenantKey struct{}

// WithSubject 将主体写入 context。
func WithSubject(ctx context.Context, sub Subject) context.Context {
	return context.WithValue(ctx, ctxKey{}, sub)
}

// FromContext 读取主体。
func FromContext(ctx context.Context) (Subject, bool) {
	sub, ok := ctx.Value(ctxKey{}).(Subject)
	return sub, ok
}

// WithTenant 将本次请求所在的租户写入 context，通常由 rest.Options.Tenant 或 realtime.Options.Tenant 完成。
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// TenantFromContext 返回本次请求所在的租户；没有设置时为 ""，即平台本身。
func TenantFromContext(ctx context.Context) string {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	return tenant
}
