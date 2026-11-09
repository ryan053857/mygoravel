package rest

import (
	"log/slog"
	"net/http"
	"reflect"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
)

// Mode 控制登记行为。
type Mode int

const (
	// ModeServe 为运行模式：调用 newHandler 并挂载处理器。
	ModeServe Mode = iota
	// ModeOpenAPI 为文档模式：生成 schema，使用内部占位处理器。
	ModeOpenAPI
)

// API 包装 huma.API，携带模式与实例级策略。
type API struct {
	huma.API
	mode      Mode
	router    chi.Router
	logger    *slog.Logger
	languages *goi18n.Bundle
}

// Options 创建 API。
type Options struct {
	Title   string
	Version string
	Mode    Mode
	// HTTP 提供请求超时、请求体上限与可信代理；零值字段使用 httpx.DefaultConfig。
	HTTP httpx.Config
	// Logger 记录访问日志、panic 与 5xx 错误；可空。
	Logger *slog.Logger
	// Languages 用于语言协商与错误消息本地化；可空。
	Languages *goi18n.Bundle
	// Transformers 追加在 Huma 默认 Transformer 之后。
	Transformers []huma.Transformer
	// Router 可选；nil 时使用 chi.NewRouter()。
	Router chi.Router
}

// New 创建 chi + Huma API。
func New(opts Options) *API {
	if opts.Logger != nil {
		opts.Logger = slog.New(logging.NewContextHandler(opts.Logger.Handler()))
	}
	if opts.Title == "" {
		opts.Title = "API"
	}
	if opts.Version == "" {
		opts.Version = "0.0.0"
	}
	r := opts.Router
	if r == nil {
		r = chi.NewRouter()
	}
	r.Use(httpx.Middleware(opts.HTTP, httpx.MiddlewareOptions{Logger: opts.Logger, Languages: opts.Languages}))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	cfg := huma.DefaultConfig(opts.Title, opts.Version)
	// huma.ErrorModel 的 JSON 是 ErrorModel 的合法实例，因此文档统一描述为 ErrorModel；
	// 不覆盖全局 huma.NewError，避免影响同一进程中的其他 Huma API。
	cfg.OpenAPI.Components.Schemas.RegisterTypeAlias(reflect.TypeOf(huma.ErrorModel{}), reflect.TypeOf(ErrorModel{}))
	cfg.Transformers = append(cfg.Transformers, opts.Transformers...)
	h := humachi.New(r, cfg)
	return &API{API: h, mode: opts.Mode, router: r, logger: opts.Logger, languages: opts.Languages}
}

// Mode 返回当前模式。
func (a *API) Mode() Mode { return a.mode }

// Router 返回底层 chi 路由，可作为 http.Handler。
func (a *API) Router() chi.Router { return a.router }

// Handler 返回 HTTP 处理器。
func (a *API) Handler() http.Handler { return a.router }

// OpenAPIJSON 返回当前 OpenAPI 文档的 JSON。
func (a *API) OpenAPIJSON() ([]byte, error) {
	return a.OpenAPI().MarshalJSON()
}
