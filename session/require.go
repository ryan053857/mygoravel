package session

import (
	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// Resolver 在每个请求中把登录时保存的主体转换为当前的主体，例如重新查询用户的状态和权限。
// 返回 errorx.Unauthenticated 类错误或 ID 为空的主体时请求得到 401，返回 5xx 类错误时得到对应的状态码。
type Resolver func(ctx huma.Context, sub identity.Subject) (identity.Subject, error)

// Stored 是原样使用登录时保存的主体的 Resolver，不做任何查询。
func Stored(_ huma.Context, sub identity.Subject) (identity.Subject, error) {
	return sub, nil
}

// Require 要求请求属于已登录的会话，并且 resolve 返回的主体具备 scopes 中的全部权限。
// 它与 rest.RequireBearer 对称：在 OpenAPI 中登记一个 cookie 安全方案，方案名即会话 cookie 名，
// 运行时把主体写入 identity.WithSubject，主体的 Method 为 session；未登录返回 401，权限不足返回 403。
func Require(api huma.API, sm *scs.SessionManager, resolve Resolver, scopes ...string) func(op *huma.Operation) {
	if sm == nil {
		panic("session: nil session manager")
	}
	if resolve == nil {
		panic("session: nil resolver")
	}
	name := sm.Cookie.Name
	scheme := &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: name}
	return rest.RequireAuth(api, name, scheme, func(ctx huma.Context) (identity.Subject, error) {
		stored, ok := Subject(ctx.Context(), sm)
		if !ok {
			return identity.Subject{}, nil
		}
		sub, err := resolve(ctx, stored)
		sub.Method = "session"
		return sub, err
	}, scopes...)
}
