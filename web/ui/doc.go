// Package ui 内置 shadcn-templ 组件的样式和脚本，把 web 的页面接到组件上：
// 不需要安装 Tailwind、shadcn-templ 命令行，也不需要写衔接脚本。
//
// 组件直接 import shadcn-templ，例如 github.com/axadrn/shadcn-templ/v2/components/button；
// 版本由框架的 go.mod 固定，样式表和脚本按这个版本预先生成（ShadcnTemplVersion）。
// ui 只提供衔接：
//
//   - Head、Page：按 web.ThemeFrom 的主题加载样式表、主题变量、htmx、Alpine 和组件脚本；
//   - Toaster：用 toast 显示 web.Flash 的提示和局部请求的错误；
//   - Modal：放置服务端渲染的对话框，web.CloseDialog 关闭它；
//   - ErrorPage：web.Options.ErrorPage 的默认实现。
//
// 每一项都可以不用：例如租户端用自己的提示组件监听 web:flash，或者自己写 <html> 只调用 Head。
//
//	pages := web.New(web.Options{
//		Sessions:  sessions,
//		Login:     "/login",
//		ErrorPage: ui.ErrorPage,
//		Theme:     cfg.Theme,
//	})
//	router.Handle(web.AssetsPath+"*", web.Assets())
//	router.Handle(ui.AssetsPath+"*", ui.Assets())
package ui

//go:generate go tool templ generate -f ui.templ
//go:generate go run ./internal/gen
