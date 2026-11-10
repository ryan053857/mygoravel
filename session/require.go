package session

import (
	"github.com/alexedwards/scs/v2"
	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// Require 要求请求属于已登录的会话，并具备 scopes 中的全部权限。
// 它与 rest.RequireBearer 对称：在 OpenAPI 中登记名为 session 的 cookie 安全方案，
// 运行时把会话中的主体写入 identity.WithSubject；未登录返回 401，权限不足返回 403。
func Require(api huma.API, sm *scs.SessionManager, scopes ...string) func(op *huma.Operation) {
	if sm == nil {
		panic("session: nil session manager")
	}
	scheme := &huma.SecurityScheme{Type: "apiKey", In: "cookie", Name: sm.Cookie.Name}
	return rest.RequireAuth(api, "session", scheme, func(ctx huma.Context) (identity.Subject, error) {
		sub, _ := Subject(ctx.Context(), sm)
		return sub, nil
	}, scopes...)
}
