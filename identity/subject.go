package identity

import (
	"context"
	"strings"
)

// SubjectType 描述主体类别。
type SubjectType string

const (
	SubjectUser    SubjectType = "user"
	SubjectService SubjectType = "service"
)

// Subject 是已认证主体；ID 为不透明标识。
//
// Permissions 是主体在 Tenant 中拥有的权限，Scopes 是本次请求所用凭据被限制在内的权限，
// 两者共同决定 Can：例如用户拥有 articles:*，他创建的只读令牌的 Scopes 为 articles:read。
//
// Actor 不为空表示代为登录：实际操作的人是 Actor，他以 ID 的身份和权限操作，例如客服代用户登录排查问题。
// Actor 由凭据记录，审计日志应同时记录 ID 和 Actor。
type Subject struct {
	ID          string
	Type        SubjectType
	Actor       string   // 代为登录时实际操作者的 ID；为空表示主体本人
	Method      string   // 认证方式，如 bearer、session
	Credential  string   // 本次请求所用凭据的 ID，如 API token 的 ID；用于登出当前设备、在设备列表中标出当前设备，可为空
	Tenant      string   // 本次请求所在的租户，"" 表示平台本身；由凭据设置时，凭据只能在这个租户中使用
	Permissions []string // 主体在 Tenant 中拥有的权限，支持通配符，见 Can
	Scopes      []string // 凭据的权限限制，支持通配符；nil 表示不限制，空切片表示什么都不允许
}

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

// WithTenant 将本次请求所在的租户写入 context，通常由 rest.Options.Tenant 或 web.Options.Tenant 完成。
func WithTenant(ctx context.Context, tenant string) context.Context {
	return context.WithValue(ctx, tenantKey{}, tenant)
}

// TenantFromContext 返回本次请求所在的租户；没有设置时为 ""，即平台本身。
func TenantFromContext(ctx context.Context) string {
	tenant, _ := ctx.Value(tenantKey{}).(string)
	return tenant
}
