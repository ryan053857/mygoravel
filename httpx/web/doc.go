// Package web 渲染 HTML 页面（组件通常由 templ 生成），配合内嵌的 htmx 4 与 Alpine.js。
//
// New 按错误页组件创建 Pages，Pages.Handle 把返回 error 的处理器适配为 http.HandlerFunc：
// 错误按 errorx 分类映射状态码，局部 htmx 请求渲染错误片段，其余请求渲染整页错误；
// htmx 请求收到的 3xx 跳转自动改写为 HX-Redirect。
// Render 渲染组件，ParseForm 解码并按 Huma 标签验证表单，SSE 推送服务端事件，
// Assets 与 Scripts 提供内嵌的 htmx、hx-sse、hx-alpine-compat 与 Alpine.js。
//
//	pages := web.New(web.Options{
//		ErrorPage:     views.ErrorPage,
//		ErrorFragment: views.ErrorFragment,
//		ErrorTarget:   "#flash",
//	})
//	router.Handle(web.AssetsPath+"*", web.Assets())
//	router.Get("/articles/{id}", pages.Handle(h.Show))
package web
