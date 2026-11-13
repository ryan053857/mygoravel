package web

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/a-h/templ"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/i18n"
	"github.com/go-tenon/tenon/logging"
)

// ErrNotFound 是页面不存在的错误，Pages.NotFound 呈现它。
var ErrNotFound = errorx.Define(errorx.NotFound, "web.not_found")

// HandlerFunc 是返回错误的页面处理器。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Mode 控制 Pages 的用途。
type Mode int

const (
	// ModeServe 为运行模式：注册真正的处理器。
	ModeServe Mode = iota
	// ModeRoutes 为路由表模式：只登记路由，供 routes 命令列出；不需要错误组件，处理器不会被调用。
	ModeRoutes
)

// Options 创建 Pages。运行模式下 ErrorPage 必须设置，其余可空。
//
// 一个应用可以有多个 Pages，例如官网和帮助中心：各自的主题和错误呈现。
type Options struct {
	Mode Mode
	// ErrorPage 渲染整页错误，用于普通请求和整页 htmx 请求，例如 ui.ErrorPage。
	// message 来自 httpx.ErrorDetail：业务错误的译文，或状态描述。
	ErrorPage func(status int, message string) templ.Component
	// ErrorFragment 渲染局部 htmx 请求的错误，替换到 ErrorTarget。nil 时（默认）不替换页面内容，
	// 错误作为 Type 为 error 的提示随 FlashEvent 送达。
	ErrorFragment func(status int, message string) templ.Component
	// ErrorTarget 是错误片段的替换位置（CSS 选择器）。设置了 ErrorFragment 时必填，它应当存在于每个页面的布局中。
	ErrorTarget string
	// Theme 是这组页面的外观，由 web/ui 渲染：页面路由和错误页的组件用 ThemeFrom 读取。
	Theme Theme
	// Static 是应用的静态资源，见 NewStatic；页面和错误页的组件用 StaticURL 取地址。可空。
	Static *Static
	// CSP 返回页面响应的 Content-Security-Policy，nonce 是本次请求随机生成的、已写入 templ.WithNonce 的值。
	// nil 时使用 DefaultCSP；返回空字符串时不设置。例如允许合作方的网站嵌入页面：
	//
	//	CSP: func(nonce string) string {
	//		return strings.Replace(web.DefaultCSP(nonce), "frame-ancestors 'none'", "frame-ancestors https://partner.example.com", 1)
	//	},
	//
	// 页面响应同时带有 Referrer-Policy: same-origin，跳转到其他网站时不带上本站的地址。
	CSP func(nonce string) string
	// Logger 记录 5xx 错误；nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Pages 注册页面路由：为每条路由设置内容安全策略和主题，并统一呈现错误。可以并发使用。
type Pages struct {
	opts   Options
	logger *slog.Logger
}

// New 创建 Pages。运行模式下缺少必填字段时 panic。
func New(opts Options) *Pages {
	if opts.Mode == ModeRoutes {
		return &Pages{opts: opts, logger: logging.Or(opts.Logger)}
	}
	if opts.ErrorPage == nil {
		panic("web: ErrorPage is required")
	}
	if opts.ErrorFragment != nil && opts.ErrorTarget == "" {
		panic("web: ErrorTarget is required with ErrorFragment")
	}
	return &Pages{opts: opts, logger: logging.Or(opts.Logger).With(logging.AttrComponent, "http")}
}

// handle 把 fn 适配为 http.Handler。
//
// fn 返回的错误交给 Error 呈现；响应已经开始写出时（例如渲染中途出错）只记录日志。
// htmx 请求收到 3xx 跳转时（例如 fn 调用了 http.Redirect），改写为 HX-Redirect：
// fetch 会自动跟随 3xx，跳转后的整页会被当作片段放进目标元素。
func (p *Pages) handle(fn HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rw := &responseWriter{ResponseWriter: w, r: r}
		err := fn(rw, r)
		if err == nil {
			return
		}
		if rw.wrote {
			p.logger.ErrorContext(r.Context(), "page handler failed after response started", "error", err)
			logging.RecordError(trace.SpanFromContext(r.Context()), err)
			return
		}
		p.Error(rw, r, err)
	}
}

// Error 呈现 err：按 errorx 分类确定状态码，5xx 记录日志和链路上的 exception 事件（logging.RecordError），
// 带 errorx.RetryAfter 时设置 Retry-After。
// 普通请求和整页 htmx 请求渲染 ErrorPage。局部 htmx 请求：设置了 ErrorFragment 时渲染它，
// 并通过 HX-Retarget、HX-Reswap 放进 ErrorTarget；否则不替换页面内容（HX-Reswap: none），
// 错误作为 Type 为 error 的提示随 FlashEvent 送达，5xx 的提示以 ErrorReference 为说明。用于中间件等页面处理器之外的场景。
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, err error) {
	r = r.WithContext(p.secure(w, r))
	r = r.WithContext(p.withLook(r))
	status := httpx.StatusOf(err)
	if status >= http.StatusInternalServerError {
		p.logger.ErrorContext(r.Context(), "page handler failed", "error", err)
		logging.RecordError(trace.SpanFromContext(r.Context()), err)
	}
	message := httpx.ErrorDetail(r.Context(), err)
	if d := errorx.RetryAfterOf(err); d > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(d.Seconds()))))
	}
	if IsPartial(r) && p.opts.ErrorFragment == nil {
		m := Message{Type: "error", Title: message}
		if status >= http.StatusInternalServerError {
			m.Description = ErrorReference(r.Context())
		}
		w.Header().Set("HX-Reswap", "none")
		flashError(w, m)
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

// ErrorReference 返回给用户看的请求编号，例如“请求编号：9F2K…”，按请求的语言翻译；
// 没有请求 ID（未安装 httpx.AssignRequestID）时返回空字符串。
// 5xx 错误的提示和 ui.ErrorPage 带上它：用户报告问题时附上编号，按它在日志中找到这个请求和错误的堆栈。
func ErrorReference(ctx context.Context) string {
	id := httpx.RequestID(ctx)
	if id == "" {
		return ""
	}
	if s := i18n.T(ctx, "web.request_id", map[string]any{"id": id}); s != "web.request_id" {
		return s
	}
	return "Request ID: " + id
}

// NotFound 以 ErrNotFound 呈现 404，可以直接传给 chi 的 Router.NotFound。
func (p *Pages) NotFound(w http.ResponseWriter, r *http.Request) {
	p.Error(w, r, ErrNotFound.New())
}

// responseWriter 记录响应是否已经开始，把 htmx 请求的 3xx 改写为 HX-Redirect，
// 并在写出状态码之前把提示交给 htmx。
type responseWriter struct {
	http.ResponseWriter
	r     *http.Request
	wrote bool
	// messages 是随这个响应送达的提示，例如局部请求的错误。
	messages []Message
}

// flashError 让 m 随响应送达：页面处理器之内在写出状态码时送达，之外直接触发。
func flashError(w http.ResponseWriter, m Message) {
	if rw, ok := w.(*responseWriter); ok {
		rw.messages = append(rw.messages, m)
		return
	}
	trigger(w, FlashEvent, map[string]any{"messages": []Message{m}})
}

func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	deliverMessages(w, w.r, status, w.messages)
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
