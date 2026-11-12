package rest

import (
	"context"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
)

// AccessBearer 是 RequireBearer 路由在路由表中的访问策略；RequireAuth 声明的其他方案取其方案名。
const AccessBearer = "bearer"

// adapter 是 Huma 的 chi 适配器：每个操作注册为一个实现 httpx.RouteDescriber 的处理器，
// 让 httpx.Routes 能从任意上层 Router 读出访问策略。其余行为委托给 humachi。
type adapter struct {
	router chi.Router
	chi    huma.Adapter
	docs   map[string]bool
	// spec30 是 3.0 文档端点的路径和它们的处理器，代替 Huma 自带的处理器。
	spec30 map[string]func(huma.Context)
}

func newAdapter(r chi.Router, cfg huma.Config) *adapter {
	a := &adapter{router: r, chi: humachi.NewAdapter(r), docs: docRoutes(cfg)}
	if cfg.OpenAPIPath != "" {
		doc := cfg.OpenAPI
		a.spec30 = map[string]func(huma.Context){
			cfg.OpenAPIPath + "-3.0.json": spec30("application/openapi+json", func() ([]byte, error) { return without30(doc).Downgrade() }),
			cfg.OpenAPIPath + "-3.0.yaml": spec30("application/openapi+yaml", func() ([]byte, error) { return without30(doc).DowngradeYAML() }),
		}
	}
	return a
}

func (a *adapter) Handle(op *huma.Operation, handler func(huma.Context)) {
	if h, ok := a.spec30[op.Path]; ok && op.Method == http.MethodGet {
		handler = h
	}
	a.router.Method(op.Method, op.Path, &route{op: op, handler: handler, docs: a.docs[op.Path]})
}

// spec30 返回 3.0 文档端点的处理器。Huma 自带的处理器转换时保留 webhooks，而 3.0 没有这一项。
// 每次请求时转换，文档总是最新的。
func spec30(contentType string, render func() ([]byte, error)) func(huma.Context) {
	return func(ctx huma.Context) {
		data, err := render()
		if err != nil {
			ctx.SetStatus(http.StatusInternalServerError)
			return
		}
		ctx.SetHeader("Content-Type", contentType)
		_, _ = ctx.BodyWriter().Write(data)
	}
}

func (a *adapter) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.router.ServeHTTP(w, r) }

// SanitizeInternalContext 委托给 humachi，供 Huma 的 autopatch 重新分发内部请求。
func (a *adapter) SanitizeInternalContext(ctx context.Context) context.Context {
	if s, ok := a.chi.(interface {
		SanitizeInternalContext(context.Context) context.Context
	}); ok {
		return s.SanitizeInternalContext(ctx)
	}
	return ctx
}

// route 是一个 Huma 操作的处理器。
type route struct {
	op      *huma.Operation
	handler func(huma.Context)
	docs    bool
}

func (rt *route) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.handler(humachi.NewContext(rt.op, r, w))
}

// DescribeRoute 实现 httpx.RouteDescriber。文档端点为 docs；隐藏或未声明安全要求的操作为 unknown。
func (rt *route) DescribeRoute() httpx.Route {
	if rt.docs && rt.op.OperationID == "" {
		return httpx.Route{Access: httpx.AccessDocs}
	}
	access, permissions := routeAccess(rt.op)
	return httpx.Route{Access: access, Permissions: permissions, OperationID: rt.op.OperationID, Summary: rt.op.Summary}
}

func routeAccess(op *huma.Operation) (string, []string) {
	if op.Security == nil {
		return httpx.AccessUnknown, nil
	}
	if len(op.Security) == 0 {
		return httpx.AccessPublic, nil
	}
	if name := authScheme(op); name != "" {
		permissions, _ := op.Extensions["x-required-permissions"].([]string)
		return name, slices.Clone(permissions)
	}
	return httpx.AccessUnknown, nil
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
