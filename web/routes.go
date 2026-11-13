package web

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/go-tenon/tenon/httpx"
)

// Routes 在一个 chi.Router 上注册页面路由。
type Routes struct {
	pages  *Pages
	router chi.Router
}

// Routes 返回在 r 上注册页面路由的注册器。页面都是公开的，没有会话和登录：需要登录的界面是单页应用，调用 JSON API。
// 只给部分页面加上 HTTP Basic 认证等中间件时，传入 r.With(…)。
//
// 注册的每条路由设置内容安全策略和主题，调用处理器并呈现它返回的错误。
func (p *Pages) Routes(r chi.Router) *Routes {
	return &Routes{pages: p, router: r}
}

// Get 注册 GET 路由。
func (rt *Routes) Get(path string, h HandlerFunc) { rt.Method(http.MethodGet, path, h) }

// Post 注册 POST 路由。
func (rt *Routes) Post(path string, h HandlerFunc) { rt.Method(http.MethodPost, path, h) }

// Put 注册 PUT 路由。
func (rt *Routes) Put(path string, h HandlerFunc) { rt.Method(http.MethodPut, path, h) }

// Patch 注册 PATCH 路由。
func (rt *Routes) Patch(path string, h HandlerFunc) { rt.Method(http.MethodPatch, path, h) }

// Delete 注册 DELETE 路由。
func (rt *Routes) Delete(path string, h HandlerFunc) { rt.Method(http.MethodDelete, path, h) }

// Method 注册 method 路由。
func (rt *Routes) Method(method, path string, h HandlerFunc) {
	if h == nil {
		panic("web: nil page handler")
	}
	rt.router.Method(method, path, &pageRoute{handler: rt.pages.route(h)})
}

func (p *Pages) route(h HandlerFunc) http.Handler {
	if p.opts.Mode == ModeRoutes {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web: page registered in ModeRoutes", http.StatusInternalServerError)
		})
	}
	handler := p.handle(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r = r.WithContext(p.secure(w, r))
		handler.ServeHTTP(w, r.WithContext(p.withLook(r)))
	})
}

// pageRoute 是一条页面路由的处理器，向 httpx.Routes 报告访问策略。
type pageRoute struct {
	handler http.Handler
}

func (pr *pageRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) { pr.handler.ServeHTTP(w, r) }

// DescribeRoute 实现 httpx.RouteDescriber：页面路由都是 public。
func (pr *pageRoute) DescribeRoute() httpx.Route {
	return httpx.Route{Access: httpx.AccessPublic}
}
