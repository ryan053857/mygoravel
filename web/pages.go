package web

import (
	"context"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"
	"github.com/cockroachdb/errors"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/session"
)

var (
	// ErrNotFound 是页面不存在的错误，Pages.NotFound 呈现它。
	ErrNotFound = errorx.Define(errorx.NotFound, "web.not_found")
	// ErrForbidden 表示已登录但不能访问：会话绑定了其他租户，或缺少所需的权限。它就是 identity.ErrForbidden。
	ErrForbidden = identity.ErrForbidden
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

// Options 创建 Pages。运行模式下 Sessions、Login、ErrorPage 必须设置，其余可空。
//
// 一个应用可以有多个 Pages，例如运营后台和租户端：各自的会话 cookie、登录页、主题和错误呈现。
type Options struct {
	Mode Mode
	// Sessions 是页面使用的会话，由 session.New 创建。Pages 接管它的错误处理：
	// 会话存储出错时记录日志并呈现 500 错误页。
	Sessions *scs.SessionManager
	// Login 是登录页地址。Require 的路由把未登录的请求带到这里，并在 next 参数中附上原地址。
	// 可以包含路径参数的占位，按当前请求的同名参数替换，例如租户在路径中时写 /t/{tenant}/login。
	Login string
	// Tenant 从请求中取出本次请求所在的租户，例如路径参数 chi.URLParam(r, "tenant")、子域名或自定义域名；
	// 结果在每条页面路由（包括公开路由）开始时写入 identity.WithTenant。nil 表示始终是平台本身（""）。
	Tenant func(r *http.Request) string
	// Accounts 是这组页面接受的账号体系，以及每一种的 Resolver：Require 保护的每个请求中，按登录时保存的主体的 Type
	// 调用对应的 Resolver，得到本次请求的主体，例如重新查询用户的状态、按 identity.TenantFromContext 加载用户在租户中的权限；
	// Resolver 为 nil 时直接使用保存的主体。Type 不在其中的会话按未登录处理：销毁会话并跳转到登录页。
	// Resolver 返回 errorx.Unauthenticated 类错误或 ID 为空的主体表示主体已失效，同样销毁会话；
	// 其他错误按分类呈现错误页，例如不是租户成员时返回 errorx.NotFound 得到 404。
	// 权限检查使用返回的主体；其中 Type、ID、Method、Credential、Scopes、Impersonator 总是取自会话。
	//
	// 运行模式下注册 Require 的路由时必须设置；名称不合法时 New panic。
	Accounts identity.Accounts
	// ErrorPage 渲染整页错误，用于普通请求和整页 htmx 请求，例如 ui.ErrorPage。
	// message 来自 httpx.ErrorDetail：业务错误的译文，或状态描述。
	ErrorPage func(status int, message string) templ.Component
	// ErrorFragment 渲染局部 htmx 请求的错误，替换到 ErrorTarget。nil 时（默认）不替换页面内容，
	// 错误作为 Type 为 error 的提示随 FlashEvent 送达，与 Flash 存入的提示显示在同一处。
	ErrorFragment func(status int, message string) templ.Component
	// ErrorTarget 是错误片段的替换位置（CSS 选择器）。设置了 ErrorFragment 时必填，它应当存在于每个页面的布局中。
	ErrorTarget string
	// Theme 是这组页面的外观，由 web/ui 渲染：页面路由和错误页的组件用 ThemeFrom 读取。
	Theme Theme
	// Static 是应用的静态资源，见 NewStatic；页面和错误页的组件用 StaticURL 取地址。可空。
	Static *Static
	// TrustedOrigins 是允许跨站提交的来源，例如 https://admin.example.com；可空。
	TrustedOrigins []string
	// Logger 记录 5xx 错误；nil 表示 slog.Default()。
	Logger *slog.Logger
	// Credentials 让 Require 保护的长连接（例如 SSE）在收到复查信号时立即重新校验会话，
	// 退出登录、停用用户后连接随即结束；nil 时只按 30 秒的间隔定期校验。通常是 broadcast.NewCredentials 的返回值。
	Credentials httpx.CredentialWatcher
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
	if err := opts.Accounts.Validate(); err != nil {
		panic(errors.Wrap(err, "web"))
	}
	if opts.ErrorPage == nil {
		panic("web: ErrorPage is required")
	}
	if opts.ErrorFragment != nil && opts.ErrorTarget == "" {
		panic("web: ErrorTarget is required with ErrorFragment")
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
// 主体和权限取自 Options.Accounts 中对应账号体系的 Resolver。
func (p *Pages) authorize(access Access, next http.Handler) http.Handler {
	if !access.login {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sub, ok, err := p.subject(r, access.permissions)
		if err != nil {
			p.Error(w, r, err)
			return
		}
		if !ok {
			Redirect(w, r, loginURL(r, p.opts.Login)+"?next="+url.QueryEscape(returnTo(r)))
			return
		}
		token := p.opts.Sessions.Token(r.Context())
		ctx := identity.WithSubject(r.Context(), sub)
		ctx = httpx.WithCredentialCheck(ctx, func(c context.Context) error { return p.recheck(c, token, sub, access) })
		if p.opts.Credentials != nil {
			ctx = httpx.WithCredentialWatcher(ctx, p.opts.Credentials)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// recheck 在长连接期间重新确认会话：从存储重新读取（会话可能已被登出或撤销），再执行与首次相同的检查，
// 见 identity.Reestablish。存储或 Resolve 暂时故障时返回 5xx 类错误，连接保持。
func (p *Pages) recheck(ctx context.Context, token string, was identity.Subject, access Access) error {
	lookup := func(ctx context.Context) (identity.Subject, error) {
		stored, _, err := session.Lookup(ctx, p.opts.Sessions, token)
		return stored, err
	}
	return identity.Reestablish(ctx, was, lookup, p.opts.Accounts.Resolve, access.permissions...)
}

// Login 把 sub 记为当前会话的主体，并记录 r 的客户端地址和 User-Agent 作为登录设备，见 session.Login。
// 客户端地址取自 r.RemoteAddr，经过 httpx.ClientIP 时就是真实的客户端地址。
func (p *Pages) Login(r *http.Request, sub identity.Subject) error {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		ip = r.RemoteAddr
	}
	return session.Login(r.Context(), p.opts.Sessions, sub, session.Device{IP: ip, UserAgent: r.UserAgent()})
}

// Sessions 返回 Options.Sessions，供测试等需要直接操作会话的场景使用，见 webtest.Client.Login。
func (p *Pages) Sessions() *scs.SessionManager { return p.opts.Sessions }

// Logout 销毁当前会话，见 session.Logout。
func (p *Pages) Logout(ctx context.Context) error {
	return session.Logout(ctx, p.opts.Sessions)
}

// subject 返回当前请求的主体：会话中保存的主体经过 identity.Establish，并能执行 permissions 中的全部权限。
// 未登录、会话中的主体不属于 Options.Accounts，或 Resolver 认为主体已失效（后两者销毁会话）时返回 ok 为 false；需要呈现错误页时返回 err，
// 例如保存的主体绑定了其他租户、不是租户成员、缺少权限。
func (p *Pages) subject(r *http.Request, permissions []string) (identity.Subject, bool, error) {
	ctx := r.Context()
	stored, ok := session.Subject(ctx, p.opts.Sessions)
	if !ok {
		return identity.Subject{}, false, nil
	}
	saved := func(context.Context) (identity.Subject, error) { return stored, nil }
	sub, err := identity.Establish(ctx, saved, p.opts.Accounts.Resolve, permissions...)
	switch {
	case err == nil:
		return sub, true, nil
	case identity.Failed(err):
		return identity.Subject{}, false, errors.Wrap(err, "resolve subject")
	case errors.Is(err, identity.ErrUnauthenticated):
		return identity.Subject{}, false, session.Logout(ctx, p.opts.Sessions)
	default:
		return identity.Subject{}, false, err
	}
}

var pathParam = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// loginURL 把 login 中的 {name} 替换为当前请求的路径参数 name。
func loginURL(r *http.Request, login string) string {
	return pathParam.ReplaceAllStringFunc(login, func(m string) string {
		return url.PathEscape(chi.URLParam(r, m[1:len(m)-1]))
	})
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
// htmx 请求的其他响应带上会话中的提示，见 Flash。
func (p *Pages) handle(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, r: r}
		err := fn(rw, r)
		if err == nil {
			return
		}
		if rw.wrote {
			p.logger.ErrorContext(r.Context(), "page handler failed after response started", "error", err)
			return
		}
		p.Error(rw, r, err)
	}
}

// Error 呈现 err：按 errorx 分类确定状态码，5xx 记录日志，带 errorx.RetryAfter 时设置 Retry-After。
// 普通请求和整页 htmx 请求渲染 ErrorPage。局部 htmx 请求：设置了 ErrorFragment 时渲染它，
// 并通过 HX-Retarget、HX-Reswap 放进 ErrorTarget；否则不替换页面内容（HX-Reswap: none），
// 错误作为 Type 为 error 的提示随 FlashEvent 送达。用于中间件等页面处理器之外的场景。
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, err error) {
	r = r.WithContext(p.withLook(r))
	status := httpx.StatusOf(err)
	if status >= http.StatusInternalServerError {
		p.logger.ErrorContext(r.Context(), "page handler failed", "error", err)
	}
	message := httpx.ErrorDetail(r.Context(), err)
	if d := errorx.RetryAfterOf(err); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	if IsPartial(r) && p.opts.ErrorFragment == nil {
		w.Header().Set("HX-Reswap", "none")
		flashError(w, Message{Type: "error", Title: message})
		w.WriteHeader(status)
		return
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

// responseWriter 记录响应是否已经开始，把 htmx 请求的 3xx 改写为 HX-Redirect，
// 并在写出状态码之前把会话中的提示交给 htmx。它位于会话中间件之内，取出提示的修改随会话一起保存。
type responseWriter struct {
	http.ResponseWriter
	r     *http.Request
	wrote bool
	// extra 是随这个响应送达、不经过会话的提示，例如局部请求的错误。
	extra []Message
}

// flashError 让 m 随响应送达：页面处理器之内与会话中的提示合并为一个 FlashEvent，之外直接触发。
func flashError(w http.ResponseWriter, m Message) {
	if rw, ok := w.(*responseWriter); ok {
		rw.extra = append(rw.extra, m)
		return
	}
	Trigger(w, FlashEvent, map[string]any{"messages": []Message{m}})
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	deliverFlashes(w, w.r, status, w.extra)
	if IsHTMX(w.r) && status >= 300 && status < 400 {
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
