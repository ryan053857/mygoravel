package web

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
)

// ErrNotFound 是页面不存在的错误，Pages.NotFound 返回它。
var ErrNotFound = errorx.Define(errorx.NotFound, "web.not_found")

// HandlerFunc 是返回错误的页面处理器。
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Options 创建 Pages。三个错误字段都必须设置。
type Options struct {
	// ErrorPage 渲染整页错误，用于普通请求和整页 htmx 请求。
	// message 来自 httpx.ErrorDetail：业务错误的译文，或状态描述。
	ErrorPage func(status int, message string) templ.Component
	// ErrorFragment 渲染局部 htmx 请求的错误，替换到 ErrorTarget。
	ErrorFragment func(status int, message string) templ.Component
	// ErrorTarget 是错误片段的替换位置（CSS 选择器），例如 #flash。它应当存在于每个页面的布局中。
	ErrorTarget string
	// Logger 记录 5xx 错误；nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Pages 把页面处理器适配为 http.HandlerFunc，并统一呈现错误。可以并发使用。
type Pages struct {
	opts   Options
	logger *slog.Logger
}

// New 创建 Pages。缺少错误组件或 ErrorTarget 时 panic。
func New(opts Options) *Pages {
	if opts.ErrorPage == nil || opts.ErrorFragment == nil || opts.ErrorTarget == "" {
		panic("web: ErrorPage, ErrorFragment and ErrorTarget are required")
	}
	return &Pages{opts: opts, logger: logging.Or(opts.Logger).With(logging.AttrComponent, "http")}
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

// Error 呈现 err：按 errorx 分类确定状态码，5xx 记录日志。局部 htmx 请求渲染 ErrorFragment，
// 并通过 HX-Retarget、HX-Reswap 放进 ErrorTarget；其余请求渲染 ErrorPage。
// 用于中间件等 Handle 之外的场景。
func (p *Pages) Error(w http.ResponseWriter, r *http.Request, err error) {
	status := httpx.StatusOf(err)
	if status >= http.StatusInternalServerError {
		p.logger.ErrorContext(r.Context(), "page handler failed", "error", err)
	}
	message := httpx.ErrorDetail(r.Context(), err)
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
