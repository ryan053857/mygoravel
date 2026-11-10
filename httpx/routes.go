package httpx

import (
	"cmp"
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"
)

// Route 描述一条已注册的路由。
type Route struct {
	Method string
	Path   string
	// Access 是路由声明的访问策略：AccessPublic、AccessDocs、AccessUnknown，
	// 或认证方式的名字，例如 JSON API 的 bearer、页面的 session。
	// 没有可识别声明的路由一律为 AccessUnknown，它不表示允许匿名访问。
	Access string
	// Permissions 是需要认证的路由要求调用者能执行的全部权限。
	Permissions []string
	// OperationID、Summary 来自 JSON API 的 huma.Operation，页面路由没有。
	OperationID string
	Summary     string
}

// Route.Access 的通用取值。
const (
	AccessPublic  = "public"  // 显式声明为公开
	AccessDocs    = "docs"    // Huma 自动注册的文档端点，例如 /docs、/openapi.json
	AccessUnknown = "unknown" // 无法从声明确定策略，不表示允许匿名访问
)

// RouteDescriber 由路由的处理器实现，向 Routes 报告访问策略等信息。
// httpx/rest 注册的 API 路由、web 注册的页面路由和 web.Assets 都实现了它。
type RouteDescriber interface {
	// DescribeRoute 返回除 Method、Path 以外的字段。
	DescribeRoute() Route
}

// Routes 遍历 router（包括挂载的子路由），返回按路径、方法排序的路由表。
// 处理器实现 RouteDescriber 时使用它的描述，否则 Access 为 AccessUnknown。
// 不调用任何处理器；挂载的不透明 http.Handler 显示为一条 /prefix/* 路由。
func Routes(router chi.Routes) []Route {
	var routes []Route
	// Walk 只会返回回调产生的错误；这里的回调始终成功。
	_ = chi.Walk(router, func(method, path string, handler http.Handler, _ ...func(http.Handler) http.Handler) error {
		route := Route{Access: AccessUnknown}
		if d, ok := handler.(RouteDescriber); ok {
			route = d.DescribeRoute()
			route.Permissions = slices.Clone(route.Permissions)
		}
		route.Method, route.Path = method, path
		routes = append(routes, route)
		return nil
	})
	slices.SortFunc(routes, func(a, b Route) int {
		return cmp.Or(cmp.Compare(a.Path, b.Path), cmp.Compare(a.Method, b.Method))
	})
	return routes
}
