// Package web 用 templ 组件渲染 HTML 页面，配合内嵌的 htmx 4 与 Alpine.js。
//
// 页面使用会话：New 接收 session.New 创建的会话管理器。页面路由只能通过 Pages.Routes 注册，
// 并必须声明访问策略（Public 或 Require）；每条路由自动防御跨站请求伪造、加载会话、检查登录
// （Options.Resolve 可在每个请求中重新加载主体），
// 并呈现处理器返回的错误：错误按 errorx 分类映射状态码，局部 htmx 请求渲染错误片段，
// 其余请求渲染整页错误；htmx 请求收到的 3xx 跳转自动改写为 HX-Redirect。
// 页面路由实现 httpx.RouteDescriber，routes 命令可以列出它们；ModeRoutes 只登记路由，不需要会话。
//
// Render 渲染组件，ParseForm 解码并按 Huma 标签验证表单，SSE 推送服务端事件，
// Assets 与 Scripts 提供内嵌的 htmx、hx-sse、hx-alpine-compat 与 Alpine.js。
//
//	pages := web.New(web.Options{
//		Sessions:      sessions,
//		Login:         "/login",
//		ErrorPage:     views.ErrorPage,
//		ErrorFragment: views.ErrorFragment,
//		ErrorTarget:   "#flash",
//	})
//	router.Handle(web.AssetsPath+"*", web.Assets())
//	editor := pages.Routes(router, web.Require("articles"))
//	editor.Get("/admin/articles/{id}/edit", h.Edit)
package web
