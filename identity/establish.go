package identity

import (
	"context"

	"github.com/ryan053857/mygoravel/errorx"
)

var (
	// ErrUnauthenticated 表示没有有效的凭据：没有携带、已经失效，或主体已不能使用。
	ErrUnauthenticated = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")
	// ErrForbidden 表示凭据有效但不能执行操作：凭据绑定了其他租户，或主体缺少所需的权限。
	ErrForbidden = errorx.Define(errorx.PermissionDenied, "auth.forbidden")
)

// Authenticator 识别调用者，返回凭据给出的主体。没有凭据或凭据无效时返回 ID 为空的主体或 4xx 类错误；
// 认证后端故障时返回 errorx.Unavailable、errorx.Internal 类错误或普通错误。
type Authenticator func(ctx context.Context) (Subject, error)

// Resolver 把凭据给出的主体转换为本次请求的主体，例如按 TenantFromContext 加载主体在租户中的权限。
// 返回 errorx.Unauthenticated 类错误或 ID 为空的主体表示主体已失效；其他 4xx 类错误按其分类处理，
// 例如不是租户成员时返回 errorx.NotFound。返回的主体中 Type、ID、Method、Credential、Scopes、Impersonator
// 总是取自凭据，Resolver 改变它们不起作用：它只加载权限和租户，不能把主体换成另一个人。
// 入口通过 Accounts 为每套账号体系指定 Resolver。
type Resolver func(ctx context.Context, sub Subject) (Subject, error)

// Establish 得到本次请求的主体，JSON API、页面和实时频道的入口都用它。依次：
//
//   - authenticate 识别调用者，没有有效的凭据时返回 ErrUnauthenticated；
//   - 凭据设置了 Tenant 且与本次请求的租户（TenantFromContext）不同时返回 ErrForbidden；
//   - 调用 resolve（非 nil 时），错误的处理见 Resolver；
//   - 主体不能执行 permissions 中的某项权限（Subject.Can）时返回 ErrForbidden，公开参数 permission 是这项权限。
//
// 返回的错误分三类，由入口各自呈现：认证后端或 resolve 故障时原样返回（errorx.Unavailable、errorx.Internal
// 类错误或普通错误，Failed 报告为 true）；ErrUnauthenticated；其他 4xx 类错误。
func Establish(ctx context.Context, authenticate Authenticator, resolve Resolver, permissions ...string) (Subject, error) {
	cred, err := authenticate(ctx)
	if Failed(err) {
		return Subject{}, err
	}
	if err != nil || cred.ID == "" {
		return Subject{}, ErrUnauthenticated.New()
	}
	if cred.Tenant != "" && cred.Tenant != TenantFromContext(ctx) {
		return Subject{}, ErrForbidden.New()
	}
	sub := cred
	if resolve != nil {
		sub, err = resolve(ctx, cred)
		switch {
		case Failed(err):
			return Subject{}, err
		case err != nil && errorx.KindOf(err) == errorx.Unauthenticated, err == nil && sub.ID == "":
			return Subject{}, ErrUnauthenticated.New()
		case err != nil:
			return Subject{}, err
		}
		// resolve 加载权限和租户；主体本身、认证方式、凭据、凭据的限制和代为登录者由凭据决定，不随 resolve 改变。
		sub.Type, sub.ID = cred.Type, cred.ID
		sub.Method, sub.Credential, sub.Scopes, sub.Impersonator = cred.Method, cred.Credential, cred.Scopes, cred.Impersonator
	}
	for _, perm := range permissions {
		if !sub.Can(perm) {
			return Subject{}, ErrForbidden.New(errorx.Public("permission", perm))
		}
	}
	return sub, nil
}

// Reestablish 在长连接期间重新执行 Establish，确认 was 仍然有效：凭据被撤销、凭据换了主体或代为登录者、
// 主体不再是租户成员或失去权限时返回 4xx 类错误，连接应当结束；Failed 报告为 true 的错误表示暂时无法判断，连接保持。
// authenticate 和 resolve 收到的 ctx 中 Rechecking 报告为 true。
func Reestablish(ctx context.Context, was Subject, authenticate Authenticator, resolve Resolver, permissions ...string) error {
	sub, err := Establish(context.WithValue(ctx, recheckingKey{}, true), authenticate, resolve, permissions...)
	if err == nil && (sub.Ref() != was.Ref() || sub.Impersonator != was.Impersonator) {
		return ErrUnauthenticated.New()
	}
	return err
}

type recheckingKey struct{}

// Rechecking 报告 ctx 是否来自 Reestablish，即长连接期间对已建立连接的复查，而不是新的请求。
// 不访问存储的凭据（例如 apitoken.JWT 的 access token）据此在复查时确认凭据没有被撤销。
func Rechecking(ctx context.Context) bool {
	v, _ := ctx.Value(recheckingKey{}).(bool)
	return v
}

// Failed 报告 err 是否表示认证后端或 Resolver 的故障（errorx.Unavailable、errorx.Internal 类错误或普通错误），
// 而不是对调用者的拒绝。err 为 nil 时返回 false。
func Failed(err error) bool {
	if err == nil {
		return false
	}
	kind := errorx.KindOf(err)
	return kind == errorx.Unavailable || kind == errorx.Internal
}
