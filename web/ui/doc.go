// Package ui 内置 shadcn-templ 组件的样式和脚本，把 web 的页面接到组件上：
// 不需要安装 Tailwind、shadcn-templ 命令行，也不需要写衔接脚本。
//
// 组件直接 import shadcn-templ，例如 github.com/axadrn/shadcn-templ/v2/components/button；
// 版本由框架的 go.mod 固定，样式表和脚本按这个版本预先生成（ShadcnTemplVersion）。
// 组件中的文字按请求的语言（i18n）显示。ui 只提供衔接：
//
//   - Head、Page：按 web.ThemeFrom 的主题加载样式表、主题变量、htmx、Alpine 和组件脚本；
//   - Toaster：用 toast 显示 web.Flash 的提示和局部请求的错误；
//   - Modal：放置服务端渲染的对话框，web.CloseDialog 关闭它；
//   - Confirm：hx-confirm 弹出确认对话框，代替浏览器的确认框；
//   - 请求进行中的按钮自动禁用并显示加载图标（衔接脚本，不需要组件）；
//   - ErrorPage：web.Options.ErrorPage 的默认实现。
//   - TextField、SelectField、CheckboxField 等表单字段：读取 web.Form 的输入和错误；
//   - Pagination：page.Offset 的分页导航。
//   - AppShell：后台的侧边栏外壳，导航按权限过滤、高亮当前页面；AuthCard：登录等页面的居中卡片；
//     ThemeToggle：暗色模式开关。
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

//go:generate go tool templ generate
//go:generate go run ./internal/gen
