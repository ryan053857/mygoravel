// Package docs 把一个目录中的 Markdown 文件变成带导航的文档页，例如产品的帮助中心或内部手册。
// 页面用 web/ui 的主题和组件渲染：左侧是按目录生成的侧边栏，正文右侧是本页目录，
// 切换页面时 htmx 只替换正文区域。
//
// 目录就是侧边栏：
//
//	help/
//	  _index.md              首页
//	  intro.md               /help/intro
//	  billing/
//	    _index.md            分组的标题和排序；有正文时也是一页
//	    invoices.md          /help/billing/invoices
//	    images/invoice.png   正文中用相对路径引用
//
// 文件开头可以写 front matter，三个字段都可以省略：
//
//	---
//	title: 开具发票            默认取开头的一级标题，再没有时取文件名
//	description: 申请和下载发票  显示在标题下方和目录的页面列表中
//	order: 2                  写了的按从小到大排在前面，没写的排在后面，再按文件名排序
//	---
//
// 第一层目录是侧边栏的分组，第二层是分组中可以展开的一项，不能再深。
// 目录的 _index.md 没有正文时，访问目录显示其中的页面列表。以 . 开头的文件和目录被忽略。
//
// Markdown 支持 GFM（表格、任务列表、删除线、自动链接）、标题 ID（自动生成或 {#id}）、
// GitHub 风格的提示块（> [!NOTE]、[!TIP]、[!IMPORTANT]、[!WARNING]、[!CAUTION]）和代码高亮。
// 指向其他 .md 文件、目录和图片的相对链接改写为页面地址；指向的文件不存在时 New 返回错误。
// 默认去掉 Markdown 中的 HTML，Options.HTML 打开。
//
//	//go:embed help
//	var helpFS embed.FS
//
//	content, _ := fs.Sub(helpFS, "help")
//	site, err := docs.New(docs.Options{Content: content, Prefix: "/help", Title: "帮助中心"})
//	site.Register(pages.Routes(router, web.Public))
package docs
