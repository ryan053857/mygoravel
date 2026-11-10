package web

import (
	"net/http"
	"net/url"
	"slices"

	"github.com/alexedwards/scs/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/session"
)

// ErrForbidden 表示已登录但缺少所需的 scope。
var ErrForbidden = errorx.Define(errorx.PermissionDenied, "auth.forbidden")

// RequirePage 要求请求属于已登录的会话，并具备 scopes 中的全部权限。
// 未登录时跳转到 login，并在 next 参数中带上原地址；htmx 请求的原地址取自 HX-Current-URL。
// 权限不足时交给 errs 渲染 403。通过后把主体写入 identity.WithSubject。
func RequirePage(sm *scs.SessionManager, login string, errs Errors, scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		forbidden := errs.Handle(func(http.ResponseWriter, *http.Request) error { return ErrForbidden.New() })
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sub, ok := session.Subject(r.Context(), sm)
			if !ok {
				back := r.URL.RequestURI()
				if IsHTMX(r) {
					if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil {
						back = u.RequestURI()
					}
				}
				Redirect(w, r, login+"?next="+url.QueryEscape(back))
				return
			}
			for _, s := range scopes {
				if !slices.Contains(sub.Scopes, s) {
					forbidden.ServeHTTP(w, r)
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithSubject(r.Context(), sub)))
		})
	}
}
