package web

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/session"
)

var (
	// ErrNotFound 是页面不存在的错误，Pages.NotFound 呈现它。
	ErrNotFound = errorx.Define(errorx.NotFound, "web.not_found")
	// ErrForbidden 表示已登录但缺少所需的权限，与 rest.RequireBearer 使用同一个错误码。
	ErrForbidden = errorx.Define(errorx.PermissionDenied, "auth.forbidden")
	// errUnauthenticated 表示会话已经失效，与 rest.RequireBearer 使用同一个错误码。
	errUnauthenticated = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")
	// ErrCrossOrigin 表示跨站的非安全请求被拒绝。
	ErrCrossOrigin = errorx.Define(errorx.PermissionDenied, "web.cross_origin")
)

// HandlerFunc 是返回错误的页面处理器。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Mode 控制 Pages 的用途。
type Mode int

const (
	// ModeServe 为运行模式：注册真正的处理器。
	ModeServe Mode = iota
	// ModeRoutes 为路由表模式：只登记路由，供 routes 命令列出；不需要会话和错误组件，处理器不会被调用。
	ModeRoutes
)

// Options 创建 Pages。运行模式下，除 Tenant、Resolve、TrustedOrigins、Logger 外都必须设置。
type Options struct {
	Mode Mode
	// Sessions 是页面使用的会话，由 session.New 创建。Pages 接管它的错误处理：
	// 会话存储出错时记录日志并呈现 500 错误页。
	Sessions *scs.SessionManager
	// Login 是登录页地址。Require 的路由把未登录的请求带到这里，并在 next 参数中附上原地址。
	Login string
	// Tenant 从请求中取出本次请求所在的租户，例如路径参数 chi.URLParam(r, "tenant")、子域名或自定义域名；
	// 结果在每条页面路由（包括公开路由）开始时写入 identity.WithTenant。nil 表示始终是平台本身（""）。
	Tenant func(r *http.Request) string
	// Resolve 在 Require 保护的每个请求中，把登录时保存的主体转换为本次请求的主体，
	// 例如重新查询用户的状态、按 identity.TenantFromContext 加载用户在租户中的权限；nil 时直接使用保存的主体。
	// 返回 errorx.Unauthenticated 类错误或 ID 为空的主体表示主体已失效：销毁会话并按未登录处理；
	// 其他错误按分类呈现错误页，例如不是租户成员时返回 errorx.NotFound 得到 404。
	// 权限检查使用返回的主体；其中 Method、Credential、Scopes、Actor 总是取自会话，Resolve 改变它们不起作用。
	Resolve func(ctx context.Context, sub identity.Subject) (identity.Subject, error)
	// ErrorPage 渲染整页错误，用于普通请求和整页 htmx 请求。
	// message 来自 httpx.ErrorDetail：业务错误的译文，或状态描述。
	ErrorPage func(status int, message string) templ.Component
	// ErrorFragment 渲染局部 htmx 请求的错误，替换到 ErrorTarget。
	ErrorFragment func(status int, message string) templ.Component
	// ErrorTarget 是错误片段的替换位置（CSS 选择器），例如 #flash。它应当存在于每个页面的布局中。
	ErrorTarget string
	// TrustedOrigins 是允许跨站提交的来源，例如 https://admin.example.com；可空。
	TrustedOrigins []string
	// Logger 记录 5xx 错误；nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Pages 注册页面路由：为每条路由加载会话、防御跨站请求伪造、检查登录，并统一呈现错误。可以并发使用。
type Pages struct {
	opts   Options
	logger *slog.Logger
	cop    *http.CrossOriginProtection
}

// New 创建 Pages。运行模式下缺少必填字段，或 TrustedOrigins 格式不正确时 panic。
func New(opts Options) *Pages {
	if opts.Mode == ModeRoutes {
		return &Pages{opts: opts, logger: logging.Or(opts.Logger)}
	}
	if opts.Sessions == nil || opts.Login == "" {
		panic("web: Sessions and Login are required")
	}
	if opts.ErrorPage == nil || opts.ErrorFragment == nil || opts.ErrorTarget == "" {
		panic("web: ErrorPage, ErrorFragment and ErrorTarget are required")
	}
	p := &Pages{opts: opts, logger: logging.Or(opts.Logger).With(logging.AttrComponent, "http")}
	opts.Sessions.ErrorFunc = func(w http.ResponseWriter, r *http.Request, err error) {
		p.Error(w, r, errors.Wrap(err, "session store"))
	}
	p.cop = http.NewCrossOriginProtection()
	for _, origin := range opts.TrustedOrigins {
		if err := p.cop.AddTrustedOrigin(origin); err != nil {
			panic(errors.Wrap(err, "web: trusted origin"))
		}
	}
	p.cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.Error(w, r, ErrCrossOrigin.New())
	}))
	return p
}

// authorize 按 access 检查登录和权限，通过后把主体写入 identity.WithSubject。
// 设置了 Options.Resolve 时，主体和权限取自它的返回值。
func (p *Pages) authorize(access Access, next http.Handler) http.Handler {
	if !access.login {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub, ok, err := p.subject(r)
		if err != nil {
			p.Error(w, r, err)
			return
		}
		if !ok {
			Redirect(w, r, p.opts.Login+"?next="+url.QueryEscape(returnTo(r)))
			return
		}
		for _, perm := range access.permissions {
			if !sub.Can(perm) {
				p.Error(w, r, ErrForbidden.New())
				return
			}
		}
		token := p.opts.Sessions.Token(r.Context())
		ctx := identity.WithSubject(r.Context(), sub)
		ctx = httpx.WithCredentialCheck(ctx, func(c context.Context) error { return p.recheck(c, token, sub, access) })
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recheck 在长连接期间重新确认会话：从存储重新读取（会话可能已被登出或撤销），检查凭据绑定的租户，
// 经过 Options.Resolve（用户可能已被停用或移出租户），再检查权限。存储或 Resolve 暂时故障时返回 5xx 类错误，连接保持。
func (p *Pages) recheck(ctx context.Context, token string, want identity.Subject, access Access) error {
	stored, ok, err := session.Lookup(ctx, p.opts.Sessions, token)
	if err != nil {
		return err
	}
	if !ok || stored.ID != want.ID || stored.Actor != want.Actor {
		return errUnauthenticated.New()
	}
	if stored.Tenant != "" && stored.Tenant != identity.TenantFromContext(ctx) {
		return ErrForbidden.New()
	}
	sub := stored
	if p.opts.Resolve != nil {
		sub, err = p.opts.Resolve(ctx, stored)
		switch {
		case err != nil && httpx.StatusOf(err) >= http.StatusInternalServerError:
			return err
		case err != nil && errorx.KindOf(err) == errorx.Unauthenticated, err == nil && sub.ID == "":
			return errUnauthenticated.New()
		case err != nil:
			return err
		}
	}
	for _, perm := range access.permissions {
		if !sub.Can(perm) {
			return ErrForbidden.New()
		}
	}
	return nil
}

// Login 把 sub 记为当前会话的主体，见 session.Login。
func (p *Pages) Login(ctx context.Context, sub identity.Subject) error {
	return session.Login(ctx, p.opts.Sessions, sub)
}

// Logout 销毁当前会话，见 session.Logout。
func (p *Pages) Logout(ctx context.Context) error {
	return session.Logout(ctx, p.opts.Sessions)
}

// subject 返回当前请求的主体：会话中保存的主体，经过 Options.Resolve 转换。
// 未登录，或 Resolve 认为主体已失效（此时销毁会话）时返回 ok 为 false；需要呈现错误页时返回 err，
// 例如保存的主体绑定了其他租户、不是租户成员。
func (p *Pages) subject(r *http.Request) (identity.Subject, bool, error) {
	ctx := r.Context()
	stored, ok := session.Subject(ctx, p.opts.Sessions)
	if !ok {
		return identity.Subject{}, false, nil
	}
	if stored.Tenant != "" && stored.Tenant != identity.TenantFromContext(ctx) {
		return identity.Subject{}, false, ErrForbidden.New()
	}
	if p.opts.Resolve == nil {
		return stored, true, nil
	}
	sub, err := p.opts.Resolve(ctx, stored)
	switch {
	case err != nil && httpx.StatusOf(err) >= http.StatusInternalServerError:
		return identity.Subject{}, false, errors.Wrap(err, "resolve subject")
	case err != nil && errorx.KindOf(err) == errorx.Unauthenticated, err == nil && sub.ID == "":
		return identity.Subject{}, false, session.Logout(ctx, p.opts.Sessions)
	case err != nil:
		return identity.Subject{}, false, err
	}
	// Resolve 加载权限和租户；认证方式、凭据、凭据的限制和代为登录者由会话决定，不随 Resolve 改变。
	sub.Method, sub.Credential, sub.Scopes, sub.Actor = stored.Method, stored.Credential, stored.Scopes, stored.Actor
	return sub, true, nil
}

// returnTo 返回登录后应回到的本站地址：htmx 请求取发起请求的页面，其余取请求本身。
func returnTo(r *http.Request) string {
	if IsHTMX(r) {
		if u, err := url.Parse(r.Header.Get("HX-Current-URL")); err == nil && u.Path != "" {
			return u.RequestURI()
		}
	}
	return r.URL.RequestURI()
}

// handle 把 fn 适配为 http.Handler。
//
// fn 返回的错误交给 Error 呈现；响应已经开始写出时（例如 SSE 中途出错）只记录日志。
// htmx 请求收到 3xx 跳转时（例如 fn 调用了 http.Redirect），改写为 HX-Redirect：
// fetch 会自动跟随 3xx，跳转后的整页会被当作片段放进目标元素。
func (p *Pages) handle(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, htmx: IsHTMX(r)}
		err := fn(rw, r)
		if err == nil {
			return
		}
		if rw.wrote {
			p.logger.ErrorContext(r.Context(), "page handler failed after response started", "error", err)
			return
		}
		p.Error(w, r, err)
	}
}

// Error 呈现 err：按 errorx 分类确定状态码，5xx 记录日志，带 errorx.RetryAfter 时设置 Retry-After。局部 htmx 请求渲染 ErrorFragment，
// 并通过 HX-Retarget、HX-Reswap 放进 ErrorTarget；其余请求渲染 ErrorPage。
// 用于中间件等页面处理器之外的场景。
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, err error) {
	status := httpx.StatusOf(err)
	if status >= http.StatusInternalServerError {
		p.logger.ErrorContext(r.Context(), "page handler failed", "error", err)
	}
	message := httpx.ErrorDetail(r.Context(), err)
	if d := errorx.RetryAfterOf(err); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	c := p.opts.ErrorPage(status, message)
	if IsPartial(r) {
		w.Header().Set("HX-Retarget", p.opts.ErrorTarget)
		w.Header().Set("HX-Reswap", "innerHTML")
		c = p.opts.ErrorFragment(status, message)
	}
	if rerr := Render(w, r, status, c); rerr != nil {
		p.logger.ErrorContext(r.Context(), "render error page failed", "error", rerr)
		http.Error(w, http.StatusText(status), status)
	}
}

// NotFound 以 ErrNotFound 呈现 404，可以直接传给 chi 的 Router.NotFound。
func (p *Pages) NotFound(w http.ResponseWriter, r *http.Request) {
	p.Error(w, r, ErrNotFound.New())
}

// responseWriter 记录响应是否已经开始，并把 htmx 请求的 3xx 改写为 HX-Redirect。
type responseWriter struct {
	http.ResponseWriter
	htmx  bool
	wrote bool
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	if w.htmx && status >= 300 && status < 400 {
		if loc := w.Header().Get("Location"); loc != "" {
			w.Header().Del("Location")
			w.Header().Set("HX-Redirect", loc)
			status = http.StatusNoContent
		}
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap 让 http.ResponseController 找到底层的 Flush、SetWriteDeadline。
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
