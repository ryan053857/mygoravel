// Package web 用 templ 组件渲染公开的 HTML 页面，例如官网、文章页、帮助中心，配合内嵌的 htmx 4 与 Alpine.js。
//
// 页面没有会话和登录：需要登录的界面是单页应用，调用 JSON API，用 Bearer 认证。
// 页面路由通过 Pages.Routes 注册，每条路由自动设置内容安全策略（DefaultCSP，nonce 写入 templ.WithNonce）和主题，
// 并呈现处理器返回的错误：错误按 errorx 分类映射状态码，整页请求渲染错误页，
// 局部 htmx 请求的错误作为提示随 FlashEvent 送达（或渲染 Options.ErrorFragment）；
// htmx 请求收到的 3xx 跳转自动改写为 HX-Redirect。一个应用可以有多组 Pages，各有 Theme 和错误呈现。
// 页面路由实现 httpx.RouteDescriber，routes 命令可以列出它们；ModeRoutes 只登记路由，不需要错误组件。
//
// Render 渲染组件，ParseForm 解码并按 Huma 标签验证表单，ParseQuery、QueryURL、PushURL 处理列表页的查询参数，
// Assets 与 Scripts 提供内嵌的 htmx、hx-sse、hx-alpine-compat 与 Alpine.js（Scripts 给标签带上 CSP nonce）；
// NewStatic 提供应用自己的静态文件，按内容加指纹，组件用 StaticURL 取地址。
// Theme 是页面的外观，由 web/ui 渲染界面组件的样式和脚本。
//
//	pages := web.New(web.Options{
//		ErrorPage: ui.ErrorPage,
//		Theme:     cfg.Theme,
//	})
//	router.Handle(web.AssetsPath+"*", web.Assets())
//	site := pages.Routes(router)
//	site.Get("/articles/{slug}", h.Show)
package web
