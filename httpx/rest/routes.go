package rest

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/danielgtaylor/huma/v2"
	"github.com/go-chi/chi/v5"
)

// Route 描述一条已注册的 HTTP 路由。
type Route struct {
	Method      string
	Path        string
	OperationID string
	Summary     string
	// Access 是路由声明的访问策略：Access* 常量之一，或 RequireAuth 声明的方案名（例如 bearer、session）。
	// 它不推断 chi 中间件的行为；没有可识别声明的路由一律为 AccessUnknown。
	Access string
	// Scopes 是需要认证的路由要求调用者具备的全部 scope。
	Scopes []string
}

// Route.Access 的取值。
const (
	AccessPublic  = "public"  // 通过 Public 显式公开
	AccessBearer  = "bearer"  // 通过 RequireBearer 要求认证；其他认证方案的取值为其方案名
	AccessDocs    = "docs"    // Huma 自动注册的文档端点，例如 /docs、/openapi.json
	AccessUnknown = "unknown" // 无法从声明确定策略，不表示允许匿名访问
)

// Routes 返回按路径、方法排序的路由快照，不调用处理器或处理器工厂。
// 结果包含原生 chi 路由和 Access 为 AccessDocs 的文档端点；隐藏或未进入 OpenAPI 的路由
// 没有 OperationID 和 Summary，Access 为 AccessUnknown。请在应用完成路由注册后调用。
func (a *API) Routes() []Route {
	var routes []Route
	// Walk 只会返回回调产生的错误；这里的回调始终成功。
	_ = chi.Walk(a.router, func(method, path string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route := Route{Method: method, Path: path, Access: AccessUnknown}
		if op := routeOperation(a.OpenAPI().Paths[path], method); op != nil {
			route.OperationID = op.OperationID
			route.Summary = op.Summary
			route.Access, route.Scopes = routeAccess(op)
		} else if a.docRoutes[path] {
			route.Access = AccessDocs
		}
		routes = append(routes, route)
		return nil
	})
	slices.SortFunc(routes, func(a, b Route) int {
		if order := cmp.Compare(a.Path, b.Path); order != 0 {
			return order
		}
		return cmp.Compare(a.Method, b.Method)
	})
	return routes
}

func routeOperation(item *huma.PathItem, method string) *huma.Operation {
	if item == nil {
		return nil
	}
	switch method {
	case http.MethodGet:
		return item.Get
	case http.MethodPost:
		return item.Post
	case http.MethodPut:
		return item.Put
	case http.MethodPatch:
		return item.Patch
	case http.MethodDelete:
		return item.Delete
	case http.MethodHead:
		return item.Head
	case http.MethodOptions:
		return item.Options
	case http.MethodTrace:
		return item.Trace
	default:
		return nil
	}
}

func routeAccess(op *huma.Operation) (string, []string) {
	if op.Security == nil {
		return AccessUnknown, nil
	}
	if len(op.Security) == 0 {
		return AccessPublic, nil
	}
	if name := authScheme(op); name != "" {
		scopes, _ := op.Extensions["x-required-scopes"].([]string)
		return name, slices.Clone(scopes)
	}
	return AccessUnknown, nil
}
