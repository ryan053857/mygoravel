package web

import (
	"net/http"
	"slices"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
)

// AccessSession 是需要登录的页面路由在路由表中的访问策略。
const AccessSession = "session"

// Access 是页面路由的访问策略，由 Public 或 Require 给出。零值不是合法的策略。
type Access struct {
	declared bool
	login    bool
	scopes   []string
}

// Public 允许未登录访问，例如登录页。
var Public = Access{declared: true}

// Require 要求请求属于已登录的会话，并具备 scopes 中的全部权限：
// 未登录时跳转到 Options.Login，并在 next 参数中带上原地址（htmx 请求取自 HX-Current-URL）；
// 缺少 scope 时呈现 403。设置了 Options.Resolve 时，主体和 scope 取自它的返回值。
// 通过后主体写入 identity.WithSubject，处理器用 identity.FromContext 读取。
func Require(scopes ...string) Access {
	return Access{declared: true, login: true, scopes: slices.Clone(scopes)}
}

// Routes 在一个 chi.Router 上注册同一访问策略的页面路由。
type Routes struct {
	pages  *Pages
	router chi.Router
	access Access
}

// Routes 返回在 r 上注册页面路由的注册器，每条路由都使用 access。
// 页面路由只能通过注册器注册，因此每条路由都显式声明了访问策略；access 是零值时 panic。
//
// 注册的每条路由依次：拒绝跨站的非安全请求（http.CrossOriginProtection，不需要 CSRF token）、
// 加载和保存会话、按 access 检查登录，最后调用处理器并呈现它返回的错误。
func (p *Pages) Routes(r chi.Router, access Access) *Routes {
	if !access.declared {
		panic("web: page routes must declare web.Public or web.Require")
	}
	return &Routes{pages: p, router: r, access: access}
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
	rt.router.Method(method, path, &pageRoute{access: rt.access, handler: rt.pages.route(rt.access, h)})
}

func (p *Pages) route(access Access, h HandlerFunc) http.Handler {
	if p.opts.Mode == ModeRoutes {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "web: page registered in ModeRoutes", http.StatusInternalServerError)
		})
	}
	return p.cop.Handler(p.opts.Sessions.LoadAndSave(p.authorize(access, p.handle(h))))
}

// pageRoute 是一条页面路由的处理器，向 httpx.Routes 报告访问策略。
type pageRoute struct {
	access  Access
	handler http.Handler
}

func (pr *pageRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) { pr.handler.ServeHTTP(w, r) }

// DescribeRoute 实现 httpx.RouteDescriber：Public 为 public，Require 为 session 及其 scope。
func (pr *pageRoute) DescribeRoute() httpx.Route {
	if !pr.access.login {
		return httpx.Route{Access: httpx.AccessPublic}
	}
	return httpx.Route{Access: AccessSession, Scopes: slices.Clone(pr.access.scopes)}
}
