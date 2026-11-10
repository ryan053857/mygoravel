// Package web 用 templ 组件渲染 HTML 页面，配合内嵌的 htmx 4 与 Alpine.js。
//
// 页面使用会话：New 接收 session.New 创建的会话管理器，Pages.Middleware 加载会话并防御跨站请求伪造，
// Pages.Require 要求登录，Options.Resolve 可在每个请求中重新加载主体。Pages.Handle 把返回 error 的处理器适配为 http.HandlerFunc：
// 错误按 errorx 分类映射状态码，局部 htmx 请求渲染错误片段，其余请求渲染整页错误；
// htmx 请求收到的 3xx 跳转自动改写为 HX-Redirect。
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
//	router.Group(func(r chi.Router) {
//		r.Use(pages.Middleware, pages.Require())
//		r.Get("/admin/articles/{id}/edit", pages.Handle(h.Edit))
//	})
package web
