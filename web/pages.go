package web

import (
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"slices"
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
	// ErrForbidden 表示已登录但缺少所需的 scope，与 rest.RequireBearer 使用同一个错误码。
	ErrForbidden = errorx.Define(errorx.PermissionDenied, "auth.forbidden")
	// ErrCrossOrigin 表示跨站请求被 Pages.Middleware 拒绝。
	ErrCrossOrigin = errorx.Define(errorx.PermissionDenied, "web.cross_origin")
)

// HandlerFunc 是返回错误的页面处理器。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Options 创建 Pages。除 Logger 外都必须设置。
type Options struct {
	// Sessions 是页面使用的会话，由 session.New 创建。Pages 接管它的错误处理：
	// 会话存储出错时记录日志并呈现 500 错误页。
	Sessions *scs.SessionManager
	// Login 是登录页地址。Require 把未登录的请求带到这里，并在 next 参数中附上原地址。
	Login string
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

// Pages 把页面处理器适配为 http.HandlerFunc，加载会话、检查登录，并统一呈现错误。可以并发使用。
type Pages struct {
	opts   Options
	logger *slog.Logger
	cop    *http.CrossOriginProtection
}

// New 创建 Pages。缺少必填字段或 TrustedOrigins 格式不正确时 panic。
func New(opts Options) *Pages {
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

// Middleware 是每个页面路由都需要的中间件：先拒绝跨站的非安全请求（http.CrossOriginProtection，
// 不需要 CSRF token），再加载和保存会话。用法：router.Use(pages.Middleware)。
func (p *Pages) Middleware(next http.Handler) http.Handler {
	return p.cop.Handler(p.opts.Sessions.LoadAndSave(next))
}

// Require 要求请求属于已登录的会话，并具备 scopes 中的全部权限。它依赖 Middleware。
//
// 未登录时跳转到 Options.Login，并在 next 查询参数中带上原地址（htmx 请求的原地址取自 HX-Current-URL），
// 登录处理器应当用 LocalPath 校验 next。权限不足时呈现 403。
// 通过后把主体写入 identity.WithSubject，处理器用 identity.FromContext 读取。
func (p *Pages) Require(scopes ...string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sub, ok := session.Subject(r.Context(), p.opts.Sessions)
			if !ok {
				Redirect(w, r, p.opts.Login+"?next="+url.QueryEscape(returnTo(r)))
				return
			}
			for _, scope := range scopes {
				if !slices.Contains(sub.Scopes, scope) {
					p.Error(w, r, ErrForbidden.New())
					return
				}
			}
			next.ServeHTTP(w, r.WithContext(identity.WithSubject(r.Context(), sub)))
		})
	}
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

// Handle 把 fn 适配为 http.HandlerFunc。
//
// fn 返回的错误交给 Error 呈现；响应已经开始写出时（例如 SSE 中途出错）只记录日志。
// htmx 请求收到 3xx 跳转时（例如 fn 调用了 http.Redirect），改写为 HX-Redirect：
// fetch 会自动跟随 3xx，跳转后的整页会被当作片段放进目标元素。
func (p *Pages) Handle(fn HandlerFunc) http.HandlerFunc {
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
// 用于中间件等 Handle 之外的场景。
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
