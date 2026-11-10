package session

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/alexedwards/scs/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/web"
	"github.com/ryan053857/mygoravel/identity"
)

// errForbidden 与 rest.RequireBearer 使用同一个错误码，译文见 i18n 内置的 auth.forbidden。
var errForbidden = errorx.Define(errorx.PermissionDenied, "auth.forbidden")

// RequirePage 是页面路由的 Require：要求请求属于已登录的会话，并具备 scopes 中的全部权限。
//
// 未登录时跳转到 login，并在 next 查询参数中带上原地址（htmx 请求的原地址取自 HX-Current-URL）；
// 登录处理器应当用 web.LocalPath 校验 next。权限不足时由 pages 呈现 403。
// 通过后把会话中的主体写入 identity.WithSubject，处理器用 identity.FromContext 读取。
func RequirePage(pages *web.Pages, sm *scs.SessionManager, login string, scopes ...string) func(http.Handler) http.Handler {
	if pages == nil || sm == nil {
		panic("session: nil pages or session manager")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sub, ok := Subject(r.Context(), sm)
			if !ok {
				web.Redirect(w, r, login+"?next="+url.QueryEscape(returnTo(r)))
				return
			}
			for _, scope := range scopes {
				if !slices.Contains(sub.Scopes, scope) {
					pages.Error(w, r, errForbidden.New())
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithSubject(r.Context(), sub)))
		})
	}
}

// returnTo 返回登录后应回到的本站地址：htmx 请求取发起请求的页面，其余取请求本身。
func returnTo(r *http.Request) string {
	if web.IsHTMX(r) {
		if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && u.Path != "" {
			return u.RequestURI()
		}
	}
	return r.URL.RequestURI()
}
