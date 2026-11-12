package rest

import (
	"log/slog"
	"net/http"
	"reflect"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
)

// Mode 控制登记行为。
type Mode int

const (
	// ModeServe 为运行模式：挂载处理器。
	ModeServe Mode = iota
	// ModeOpenAPI 为文档模式：生成 schema，使用内部占位处理器。
	ModeOpenAPI
)

// API 包装 huma.API，携带模式与实例级策略。
type API struct {
	huma.API
	mode     Mode
	router   chi.Router
	logger   *slog.Logger
	accounts identity.Accounts
	watcher  httpx.CredentialWatcher
	names    *schemaNames

	endpoints []Endpoint
}

// Options 创建 API。
type Options struct {
	Title   string
	Version string
	Mode    Mode
	// Logger 记录 5xx 错误；nil 表示 slog.Default()。
	Logger *slog.Logger
	// Transformers 追加在 Huma 默认 Transformer 之后。
	Transformers []huma.Transformer
	// BasePath 是 API 挂载的路径前缀，例如挂在 router.Route("/api", ...) 中时为 /api。
	// 它写入 OpenAPI 的 servers，文档页和客户端据此拼出完整地址；路由本身仍按 Router 注册。
	BasePath string
	// Router 可选；nil 时使用 chi.NewRouter()。
	// New 不安装任何中间件：在传入前用 Router.Use 安装，例如 httpx.Defaults。
	Router chi.Router
	// Tenant 从请求中取出本次请求所在的租户，例如路径参数 ctx.Param("tenant")、子域名或请求头；
	// 结果在每个操作（包括公开的操作）开始时写入 identity.WithTenant。nil 表示始终是平台本身（""）。
	Tenant func(ctx huma.Context) string
	// Accounts 是这个 API 接受的账号体系，以及每一种的 Resolver：认证通过后，按凭据给出的主体的 Type
	// 调用对应的 Resolver，得到本次请求的主体，例如按 identity.TenantFromContext 加载用户在租户中的权限。
	// Type 不在其中的凭据响应 401，与凭据无效相同；Resolver 为 nil 时直接使用凭据给出的主体。
	// Resolver 返回 errorx.Unauthenticated 类错误或 ID 为空的主体时响应 401，其他 4xx 类错误按其分类响应，
	// 例如不是租户成员时返回 errorx.NotFound 得到 404；5xx 类错误响应对应的状态码。
	// 返回的主体中 Type、ID、Method、Credential、Scopes、Impersonator 总是取自凭据。
	//
	// 运行模式下注册需要认证的操作（RequireAuth、RequireBearer）时必须设置；名称不合法时 New panic。
	Accounts identity.Accounts
	// Credentials 让 RequireAuth 保护的长连接（例如 SSE）在收到复查信号时立即重新校验凭据，
	// 撤销 token、停用用户后连接随即结束；nil 时只按 30 秒的间隔定期校验。通常是 broadcast.NewCredentials 的返回值。
	Credentials httpx.CredentialWatcher
}

// New 创建 chi + Huma API，并让未匹配的路由与方法返回 Problem Details。
func New(opts Options) *API {
	if err := opts.Accounts.Validate(); err != nil {
		panic(errors.Wrap(err, "rest"))
	}
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
	// schema 名带包名，不同包的同名类型（例如 article.Changes 和 comment.Changes）不会冲突，见 schemaNames。
	names := &schemaNames{anon: map[reflect.Type]string{}}
	cfg.OpenAPI.Components.Schemas = huma.NewMapRegistry("#/components/schemas/", names.name)
	// Huma 产生的错误由 localizeErrors 转为 ErrorModel，因此文档统一描述为 ErrorModel；
	// 不覆盖全局 huma.NewError，避免影响同一进程中的其他 Huma API。
	cfg.OpenAPI.Components.Schemas.RegisterTypeAlias(reflect.TypeOf(huma.ErrorModel{}), reflect.TypeOf(ErrorModel{}))
	cfg.OpenAPI.Components.Schemas.RegisterTypeAlias(reflect.TypeOf(huma.ErrorDetail{}), reflect.TypeOf(ErrorDetail{}))
	// localizeErrors 排在最前，应用的 Transformer 看到的已经是翻译后的 ErrorModel。
	cfg.Transformers = append([]huma.Transformer{localizeErrors}, opts.Transformers...)
	if opts.BasePath != "" {
		cfg.OpenAPI.Servers = []*huma.Server{{URL: opts.BasePath}}
	}
	h := huma.NewAPI(cfg, newAdapter(r, cfg))
	if tenant := opts.Tenant; tenant != nil {
		h.UseMiddleware(func(ctx huma.Context, next func(huma.Context)) {
			next(huma.WithContext(ctx, identity.WithTenant(ctx.Context(), tenant(ctx))))
		})
	}
	return &API{API: h, mode: opts.Mode, router: r, logger: opts.Logger, accounts: opts.Accounts, watcher: opts.Credentials, names: names}
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

// OpenAPI30JSON 返回转换为 OpenAPI 3.0.3 的文档，供只支持 3.0 的客户端生成器使用。
// 3.0 没有 webhooks，转换时去掉；文档端点 /openapi-3.0.json 和 /openapi-3.0.yaml 也是这样。
func (a *API) OpenAPI30JSON() ([]byte, error) {
	return without30(a.OpenAPI()).Downgrade()
}

// without30 返回去掉 3.0 不支持的 webhooks 的浅拷贝，原文档不变。
func without30(doc *huma.OpenAPI) *huma.OpenAPI {
	d := *doc
	d.Webhooks = nil
	return &d
}
