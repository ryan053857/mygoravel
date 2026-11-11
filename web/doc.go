// Package web 用 templ 组件渲染 HTML 页面，配合内嵌的 htmx 4 与 Alpine.js。
//
// 页面使用会话：New 接收 session.New 创建的会话管理器。页面路由只能通过 Pages.Routes 注册，
// 并必须声明访问策略（Public 或 Require）；每条路由自动防御跨站请求伪造、加载会话、检查登录
// （Options.Resolve 可在每个请求中重新加载主体），
// 并呈现处理器返回的错误：错误按 errorx 分类映射状态码，整页请求渲染错误页，
// 局部 htmx 请求的错误作为提示随 FlashEvent 送达（或渲染 Options.ErrorFragment）；
// htmx 请求收到的 3xx 跳转自动改写为 HX-Redirect。一个应用可以有多组 Pages，各有会话、登录页和 Theme。
// 页面路由实现 httpx.RouteDescriber，routes 命令可以列出它们；ModeRoutes 只登记路由，不需要会话。
//
// Render 渲染组件，ParseForm 解码并按 Huma 标签验证表单，ParseQuery、QueryURL、PushURL 处理列表页的查询参数，
// Flash 存入一次性提示，Trigger 让 htmx 在页面上派发事件，CloseDialog 关闭对话框，SSE 推送服务端事件，
// Assets 与 Scripts 提供内嵌的 htmx、hx-sse、hx-alpine-compat 与 Alpine.js（Scripts 支持 templ.WithNonce 设置的 CSP nonce）；
// NewStatic 提供应用自己的静态文件，按内容加指纹，组件用 StaticURL 取地址。
// Theme 是页面的外观，由 web/ui 渲染界面组件的样式和脚本。
//
//	pages := web.New(web.Options{
//		Sessions:  sessions,
//		Login:     "/login",
//		ErrorPage: ui.ErrorPage,
//		Theme:     cfg.Theme,
//	})
//	router.Handle(web.AssetsPath+"*", web.Assets())
//	editor := pages.Routes(router, web.Require("articles"))
//	editor.Get("/admin/articles/{id}/edit", h.Edit)
package web
