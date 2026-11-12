package web

import (
	"context"
	"net/http"
	"slices"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

// AccessSession 是需要登录的页面路由在路由表中的访问策略。
const AccessSession = "session"

// Access 是页面路由的访问策略，由 Public 或 Require 给出。零值不是合法的策略。
type Access struct {
	declared    bool
	login       bool
	permissions []string
	confirmed   time.Duration
}

// Public 允许未登录访问，例如登录页。
var Public = Access{declared: true}

// Require 要求请求属于已登录的会话，并且主体能执行 permissions 中的全部权限（identity.Subject.Can）：
// 未登录时跳转到 Options.Login，并在 next 参数中带上原地址（htmx 请求取自 HX-Current-URL）；
// 权限不足时呈现 403。主体和权限取自 Options.Accounts 中对应账号体系的 Resolver。
// 通过后主体写入 identity.WithSubject，处理器用 identity.FromContext 读取。
func Require(permissions ...string) Access {
	return Access{declared: true, login: true, permissions: slices.Clone(permissions)}
}

// Confirmed 在 Require 的基础上要求主体在 maxAge 之内确认过身份（登录或 Pages.Confirm），
// 例如修改邮箱、关闭二次验证、删除账号：超过时跳转到 Options.Confirm，next 参数带上原地址，
// 确认页验证密码后调用 Pages.Confirm 再跳回。对 Public 调用或 maxAge 不为正数时 panic。
func (a Access) Confirmed(maxAge time.Duration) Access {
	if !a.login || maxAge <= 0 {
		panic("web: Confirmed requires web.Require and a positive maxAge")
	}
	a.permissions = slices.Clone(a.permissions)
	a.confirmed = maxAge
	return a
}

// Routes 在一个 chi.Router 上注册同一访问策略的页面路由。
type Routes struct {
	pages  *Pages
	router chi.Router
	access Access
}

// Routes 返回在 r 上注册页面路由的注册器，每条路由都使用 access。
// 页面路由只能通过注册器注册，因此每条路由都显式声明了访问策略；access 是零值，
// 或运行模式下 access 要求登录而 Options.Accounts 为空、要求确认身份而 Options.Confirm 为空时 panic。
//
// 注册的每条路由依次：拒绝跨站的非安全请求（http.CrossOriginProtection，不需要 CSRF token）、
// 加载和保存会话、按 access 检查登录，最后调用处理器并呈现它返回的错误。
func (p *Pages) Routes(r chi.Router, access Access) *Routes {
	if !access.declared {
		panic("web: page routes must declare web.Public or web.Require")
	}
	if access.login && p.opts.Mode != ModeRoutes && len(p.opts.Accounts) == 0 {
		panic("web: Options.Accounts is required for web.Require routes")
	}
	if access.confirmed > 0 && p.opts.Mode != ModeRoutes && p.opts.Confirm == "" {
		panic("web: Options.Confirm is required for Confirmed routes")
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
	handler := p.cop.Handler(p.opts.Sessions.LoadAndSave(p.authorize(access, p.handle(h))))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(p.withLook(r), sessionsKey{}, p.opts.Sessions)
		if tenant := p.opts.Tenant; tenant != nil {
			ctx = identity.WithTenant(ctx, tenant(r))
		}
		handler.ServeHTTP(w, r.WithContext(ctx))
	})
}

// pageRoute 是一条页面路由的处理器，向 httpx.Routes 报告访问策略。
type pageRoute struct {
	access  Access
	handler http.Handler
}

func (pr *pageRoute) ServeHTTP(w http.ResponseWriter, r *http.Request) { pr.handler.ServeHTTP(w, r) }

// DescribeRoute 实现 httpx.RouteDescriber：Public 为 public，Require 为 session 及其权限。
func (pr *pageRoute) DescribeRoute() httpx.Route {
	if !pr.access.login {
		return httpx.Route{Access: httpx.AccessPublic}
	}
	return httpx.Route{Access: AccessSession, Permissions: slices.Clone(pr.access.permissions)}
}
