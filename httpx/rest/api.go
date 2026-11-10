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
	// docRoutes 是 Huma 自动注册的文档端点路径，用于在路由总览中标记它们。
	docRoutes map[string]bool
}

// Options 创建 API。
type Options struct {
	Title   string
	Version string
	Mode    Mode
	// Logger 记录 5xx 错误与缺失的错误翻译；nil 表示 slog.Default()。
	Logger *slog.Logger
	// Languages 用于错误消息本地化，语言取自 httpx.Locale 写入的 context；可空。
	Languages *goi18n.Bundle
	// Transformers 追加在 Huma 默认 Transformer 之后。
	Transformers []huma.Transformer
	// Router 可选；nil 时使用 chi.NewRouter()。
	// New 不安装任何中间件：在传入前用 Router.Use 安装，例如 httpx.Defaults。
	Router chi.Router
}

// New 创建 chi + Huma API，并让未匹配的路由与方法返回 Problem Details。
func New(opts Options) *API {
	opts.Logger = logging.Or(opts.Logger).With(logging.AttrComponent, "http")
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
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusNotFound, "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, http.StatusMethodNotAllowed, "method not allowed")
	})
	cfg := huma.DefaultConfig(opts.Title, opts.Version)
	// 默认的唯一创建钩子会在响应体加入 $schema 字段并设置 Link 响应头，对 API 使用者价值不大，不启用。
	cfg.CreateHooks = nil
	// huma.ErrorModel 的 JSON 是 ErrorModel 的合法实例，因此文档统一描述为 ErrorModel；
	// 不覆盖全局 huma.NewError，避免影响同一进程中的其他 Huma API。
	cfg.OpenAPI.Components.Schemas.RegisterTypeAlias(reflect.TypeOf(huma.ErrorModel{}), reflect.TypeOf(ErrorModel{}))
	cfg.Transformers = append(cfg.Transformers, opts.Transformers...)
	h := humachi.New(r, cfg)
	return &API{API: h, mode: opts.Mode, router: r, logger: opts.Logger, languages: opts.Languages, docRoutes: docRoutes(cfg)}
}

// docRoutes 按 Huma 的注册规则列出文档端点路径。
func docRoutes(cfg huma.Config) map[string]bool {
	paths := map[string]bool{}
	if cfg.OpenAPIPath != "" {
		for _, suffix := range []string{".json", ".yaml", "-3.0.json", "-3.0.yaml"} {
			paths[cfg.OpenAPIPath+suffix] = true
		}
	}
	if cfg.DocsPath != "" {
		paths[cfg.DocsPath] = true
	}
	if cfg.SchemasPath != "" {
		paths[cfg.SchemasPath+"/{schema}"] = true
	}
	return paths
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
