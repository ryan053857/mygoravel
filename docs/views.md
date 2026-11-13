# 视图

- [简介](#introduction)
- [安装 templ](#installing-templ)
- [布局](#layouts)
- [装配页面](#assembling-pages)
- [注册页面路由](#page-routes)
- [多组页面](#multiple-areas)
- [渲染页面](#rendering-pages)
- [局部更新](#partial-updates)
- [列表页](#list-pages)
- [错误页](#error-pages)
- [跳转](#redirects)
- [前端状态](#client-state)
- [内容安全策略](#content-security-policy)

<a id="introduction"></a>
## 简介

需要登录的界面——后台、用户端、[OAuth 同意页](oauth-server.md#consent-page)——都是[单页应用](spa.md)，调用 [JSON API](routing.md)，用 Bearer token 认证。官网、文章页、帮助中心这类**公开页面**适合在服务端渲染：搜索引擎直接读到内容，首屏不用等脚本加载。写法类似 Laravel 的 Blade 加 Livewire：

- [templ](https://templ.guide) 把 `.templ` 文件编译成带类型参数的 Go 函数，相当于 Blade 组件，错误在编译期就能发现；
- [htmx 4](https://four.htmx.org) 用 HTML 属性声明「点这里就请求某个地址，把返回的片段放到某个位置」，服务端只管返回 HTML；
- [Alpine.js](https://alpinejs.dev) 处理纯前端的小交互，例如折叠区、计数。

按钮、表格、分页这些现成的组件，见[界面组件](components.md)。

`web` 包负责把它们接到框架上：渲染 templ 组件、呈现错误、处理 htmx 的跳转、设置内容安全策略，并内嵌了 htmx 与 Alpine 的脚本。`web/ui` 包内置了界面组件的样式表和脚本，按主题加载，不需要安装 Tailwind。

页面没有会话，也没有登录：页面路由都是公开的。只给内部人员看的页面，例如[文档页](doc-pages.md)中的操作手册，用 [HTTP Basic 认证](middleware.md#basic-auth)等中间件保护，或者只在内网开放。

本章把博客读者看到的首页、文章列表和文章页做成服务端渲染的页面。表单见[表单](forms.md)。

<a id="installing-templ"></a>
## 安装 templ

templ 的运行时库随 `web` 包引入，只需要把它的命令行工具加入 go.mod：

```bash
go get -tool github.com/a-h/templ/cmd/templ
```

修改 `.templ` 文件后运行 `go tool templ generate`，它会在旁边生成同名的 `_templ.go` 文件。生成的文件请一起提交，这样构建和 CI 不需要安装 templ。开发时可以用 `go tool templ generate --watch` 自动重新生成，编辑器插件见 [templ 文档](https://templ.guide/quick-start/installation)。

<a id="layouts"></a>
## 布局

网站的页面共用一个布局。下面是 `internal/web/layout.templ`：

```templ
package web

import "github.com/go-tenon/tenon/web/ui"

templ Layout(title string) {
	@ui.Page(ui.PageProps{Title: title}) {
		<main id="main" class="mx-auto max-w-3xl p-6">
			{ children... }
		</main>
		@ui.Toaster()
	}
}
```

`ui.Page` 输出完整的 HTML 文档，`<head>` 中按[主题](components.md#themes)加载样式表、htmx、Alpine 和组件脚本。`ui.Toaster()` 把局部请求的错误显示为 toast，见[错误页](#error-pages)。错误页用 `ui.ErrorPage`，见[装配页面](#assembling-pages)。

这些都可以换成自己的组件：自己写 `<html>` 时，在 `<head>` 中调用 `@ui.Head()`；不用 `web/ui` 时，`@web.Scripts()` 只输出 htmx 与 Alpine 的 `<script>` 标签，样式由应用自己提供。

<a id="assembling-pages"></a>
## 装配页面

在入口的 [`HTTP` 方法](dependencies.md#assembling-the-application)中创建 `web.Pages`，再挂载内嵌脚本和 404 页面。下面是 `internal/bootstrap/http.go` 中 `HTTP` 方法的新写法：

```go
func (a *App) HTTP(d *command.Daemon) (func(context.Context) error, error) {
    static, err := web.NewStatic(layout.Static, web.StaticOptions{DevDir: a.cfg.StaticDir})
    if err != nil {
        return nil, err
    }
    pages := web.New(web.Options{
        Static:    static,
        ErrorPage: ui.ErrorPage,
        Theme:     a.cfg.Theme,
    })

    router := chi.NewRouter()
    router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
    router.Handle(web.StaticPath+"*", static)
    router.Handle(web.AssetsPath+"*", web.Assets())
    router.Handle(ui.AssetsPath+"*", ui.Assets())
    router.NotFound(pages.NotFound)

    router.Route("/api/v1", func(r chi.Router) {
        r.Use(httpx.Timeout(a.cfg.HTTP.Timeout))
        opts := API
        opts.Router, opts.BasePath = r, "/api/v1"
        Routes(rest.New(opts), auth.Validator(a.cfg.Auth), a.db)
    })
    router.Group(func(r chi.Router) {
        r.Use(httpx.Timeout(a.cfg.HTTP.Timeout))
        PageRoutes(r, pages, a.db)
    })

    ln, err := d.Listen("tcp", a.cfg.HTTP.Addr)
    if err != nil {
        return nil, err
    }
    return func(ctx context.Context) error {
        return httpx.Serve(ctx, a.cfg.HTTP, ln, router, httpx.ServeOptions{})
    }, nil
}

// PageRoutes 注册应用的全部页面，HTTP 方法和 DocRouter 共用它。
func PageRoutes(r chi.Router, pages *web.Pages, db *bun.DB) {
    articleweb.Register(r, pages, db)
}
```

几点说明：

- 页面和 JSON API 共用一个 Router。API 挂在 `/api/v1` 下；`BasePath` 让 OpenAPI 文档中的地址带上这个前缀，见 [API 版本](routing.md#api-versioning)。`rest.New` 返回 JSON 格式 404 的设置只作用于这个子路由，其余地址的 404 由 `pages.NotFound` 渲染成页面。
- 后台等[单页应用](spa.md)也挂在这个 Router 上，例如 `router.Handle("/admin/*", spa)`。
- `static` 提供应用自己的图片等静态文件，见[前端资源](assets.md)。`Theme` 来自配置，决定组件的风格和颜色，见[主题](components.md#themes)；`ui.Assets()` 提供它们的样式表和脚本。
- [请求超时](middleware.md#request-timeout)按路由组安装。

<a id="page-routes"></a>
## 注册页面路由

页面路由通过 `pages.Routes` 注册。下面是 `internal/article/articleweb/routes.go`：

```go
package articleweb

func Register(r chi.Router, pages *web.Pages, db *bun.DB) {
    h := &Handlers{list: article.NewList(db), get: article.NewGetBySlug(db)}
    site := pages.Routes(r)
    site.Get("/", h.Home)
    site.Get("/articles", h.List)
    site.Get("/articles/{slug}", h.Show)
}
```

页面处理器的签名是 `func(w http.ResponseWriter, r *http.Request) error`。注册的每条路由会设置[内容安全策略](#content-security-policy)和主题，调用处理器，并呈现它返回的错误。

只给内部人员看的页面，把认证中间件装在传入的路由上，例如 `pages.Routes(r.With(staffOnly))`。

[routes 命令](commands.md#routes-command)同时列出 JSON API 和页面，页面的访问策略显示为 `public`。命令使用的 `DocRouter` 以文档模式注册全部路由，不打开数据库，页面使用 `web.ModeRoutes`：

```go
// DocRouter 在文档模式下构造全部路由，routes 命令用它。
func DocRouter() chi.Routes {
    router := chi.NewRouter()
    router.Route("/api/v1", func(r chi.Router) {
        opts := API
        opts.Mode, opts.Router, opts.BasePath = rest.ModeOpenAPI, r, "/api/v1"
        DocRoutes(rest.New(opts))
    })
    PageRoutes(router, web.New(web.Options{Mode: web.ModeRoutes}), nil)
    return router
}
```

`PageRoutes` 收到的依赖都是 `nil`。处理器只是被登记，不会被调用，所以不受影响。

<a id="multiple-areas"></a>
## 多组页面

一个应用可以有几组页面，例如官网和帮助中心，各用一个 `web.Pages`，挂在各自的子路由上，各有自己的主题和错误呈现：

```go
site := web.New(web.Options{ErrorPage: ui.ErrorPage, Theme: a.cfg.Site.Theme, Static: static})
help := web.New(web.Options{ErrorPage: ui.ErrorPage, Theme: a.cfg.Help.Theme})
router.Group(func(r chi.Router) {
    articleweb.Register(r, site, a.db)
})
router.Route("/help", func(r chi.Router) {
    r.NotFound(help.NotFound)
    helpSite.Register(help.Routes(r))
})
```

主题和错误样式的差异见[不同区域的界面](components.md#areas)，`helpSite` 是[文档页](doc-pages.md)。

<a id="rendering-pages"></a>
## 渲染页面

处理器调用 action 的 `Handle`，再用 `web.Render` 渲染组件。返回的错误交给 `pages` 呈现：

```go
func (h *Handlers) Show(w http.ResponseWriter, r *http.Request) error {
    item, err := h.get.Handle(r.Context(), identity.Subject{}, chi.URLParam(r, "slug"))
    if err != nil {
        return err // 例如 article.ErrNotFound，呈现为 404 页面
    }
    return web.Render(w, r, http.StatusOK, views.Article(item))
}
```

访客没有登录，传给 action 的主体是零值：action 按匿名访客授权，例如只返回已发布的文章。

`web.Render` 先把组件渲染到缓冲区，成功后再写出。组件用请求的 context 渲染，所以组件里可以直接用 `i18n.T(ctx, ...)` 翻译文案，见[本地化](localization.md)。templ 组件里的 `ctx` 就是这个 context。

页面和 JSON API 调用的是同一组 [action](actions.md)，业务规则和[授权](authorization.md)完全相同。

<a id="partial-updates"></a>
## 局部更新

htmx 让页面上的元素直接发请求，并把响应的 HTML 放到指定位置。例如文章页底部的「相关文章」在页面显示出来之后再加载，不拖慢首屏：

```templ
<section hx-get={ "/articles/" + item.Slug + "/related" } hx-trigger="revealed" hx-swap="outerHTML"></section>
```

处理器只渲染片段：

```go
func (h *Handlers) Related(w http.ResponseWriter, r *http.Request) error {
    items, err := h.related.Handle(r.Context(), chi.URLParam(r, "slug"))
    if err != nil {
        return err
    }
    return web.Render(w, r, http.StatusOK, views.Related(items))
}
```

同一个地址既要返回整页、又要返回片段时，用 `web.IsPartial(r)` 判断：它在 htmx 请求的目标不是整个 `body`、也没有 `hx-select` 时为真。htmx 的属性、触发方式和替换方式见 [htmx 文档](https://four.htmx.org/docs)。

<a id="list-pages"></a>
## 列表页

文章列表可以搜索、排序和翻页。列表的状态全部放在地址的查询参数中，例如 `/articles?q=go&sort=title&page=2`。这样刷新、后退、复制链接都能回到同一个状态，搜索引擎也能收录每一页。

查询参数用一个结构体描述，写法与[表单](forms.md#defining-forms)相同：字段名取 `json` 标签，规则写在 Huma 标签上。查询参数都是可选的，字段要带 `omitempty`。分页嵌入 `page.OffsetParams`。下面的 `ListQuery` 放在文章模块中，同时带上 `query` 标签，[列表接口](responses.md#filtering-and-sorting)也可以直接使用它：

```go
type ListQuery struct {
    Q      string `query:"q" json:"q,omitempty"`
    Status string `query:"status" json:"status,omitempty" enum:"draft,published"`
    Sort   string `query:"sort" json:"sort,omitempty" default:"-created" enum:"-created,created,title,-title"`
    page.OffsetParams
}
```

处理器用 `web.ParseQuery` 解析请求的查询参数。空值等同于没有这个参数，没有的参数取 `default` 标签的值。参数不合法时（例如 `?page=0`）返回 `web.ErrBadQuery`，页面呈现为 `422`：

```go
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) error {
    q, err := web.ParseQuery[article.ListQuery](r)
    if err != nil {
        return err
    }
    items, total, err := h.list.Handle(r.Context(), identity.Subject{}, q)
    if err != nil {
        return err
    }
    list := page.NewOffset(items, q.OffsetParams, total)
    if web.IsPartial(r) {
        web.PushURL(w, listURL(q))
        return web.Render(w, r, http.StatusOK, views.ArticleTable(q, list))
    }
    return web.Render(w, r, http.StatusOK, views.Articles(q, list))
}
```

`article.List` 是列表 action，按筛选条件和排序查询当前页，同时返回总数，代码见[筛选与排序](responses.md#filtering-and-sorting)；匿名访客只能看到已发布的文章。筛选、排序、翻页都只替换列表区域，`web.PushURL` 让 htmx 把地址栏改为这个状态的地址。

链接由 `web.QueryURL` 生成，它是 `ParseQuery` 的逆操作。零值和等于 `default` 的参数会被省略，同一个状态总是得到同一个地址。在视图包中写几个小函数：

```go
func listURL(q article.ListQuery) string { return web.QueryURL("/articles", q) }

func pageURL(q article.ListQuery, p int) string {
    q.Page = p
    return listURL(q)
}

// sortURL 返回按某个排序参数排列、回到第一页的地址，交给 ui.SortHead。
func sortURL(q article.ListQuery) func(sort string) string {
    return func(sort string) string {
        q.Sort, q.Page = sort, 1
        return listURL(q)
    }
}
```

列表区域把筛选表单也包含在内。区域每次整体替换，表单里的排序字段就总是当前的状态；搜索框带 `hx-preserve`，替换时保留原来的元素，用户可以连续输入，不会丢失焦点。htmx 4 的属性默认不继承，区域上的 `hx-target`、`hx-swap` 要加 `:inherited`，表单和链接才会沿用它们：

```templ
templ ArticleTable(q article.ListQuery, list page.Offset[article.Article]) {
	<section id="articles" hx-target:inherited="this" hx-swap:inherited="outerHTML">
		<form hx-get="/articles" hx-trigger="input delay:300ms from:#q">
			<input id="q" name="q" type="search" value={ q.Q } hx-preserve/>
			<input type="hidden" name="sort" value={ q.Sort }/>
		</form>
		if len(list.Items) == 0 {
			@ui.Empty(ui.EmptyProps{Icon: icon.FileText, Title: "没有符合条件的文章", Description: "换个关键词试试。"})
		} else {
			<table>
				<tr>
					@ui.SortHead(q.Sort, "title", "标题", sortURL(q), ui.SortHeadProps{HTMX: true})
					@ui.SortHead(q.Sort, "created", "创建时间", sortURL(q), ui.SortHeadProps{HTMX: true, DescFirst: true})
				</tr>
				for _, item := range list.Items {
					<tr><td><a href={ templ.SafeURL("/articles/" + item.Slug) }>{ item.Title }</a></td><td>{ item.CreatedAt.Format("2006-01-02") }</td></tr>
				}
			</table>
		}
		@ui.Pagination(list, ui.PaginationProps{URL: func(p int) string { return pageURL(q, p) }, HTMX: true})
	</section>
}
```

`ui.SortHead` 是可以点击排序的表头：点击按这一列升序，再点击改为降序，当前排序的列显示方向箭头并带 `aria-sort`；`DescFirst` 让第一次点击按降序，适合时间、数量。它按 `q.Sort` 判断当前的方向（`title` 为升序，`-title` 为降序），用 `sortURL(q)` 生成下一次点击的地址。`ListQuery` 的 `enum` 要包含每一列的两个方向，例如 `title,-title,created,-created`。

`ui.Empty` 是没有数据时的提示，children 可以放操作，例如「清除搜索」链接。

`ui.Pagination` 显示上一页、下一页、首页、末页，以及当前页两侧各 1 页，中间省略；只有一页时不显示。`HTMX` 让链接同时带 `hx-get`，沿用区域上的 `hx-target`；链接也写了 `href`，不支持 htmx 的场景（例如在新标签页打开）也能正常访问。自己写分页导航时，`list.Links(1)` 返回要显示的页码，省略的部分用 `0` 表示，例如 `[1 0 9 10 11 0 20]`，`list.LastPage()` 是最后一页的页码。

两个细节：

- 表单的 `hx-trigger` 要用 `from:` 指明来源。如果写成 `input delay:300ms, change`，输入框失焦时触发的 `change` 会再提交一次，和随后点击的排序链接争抢。
- 地址交给 `web.PushURL`，元素上不写 `hx-push-url`。表单提交时会带上空字段（`?q=`），由服务端推送的地址不含这些参数。

<a id="error-pages"></a>
## 错误页

处理器返回的错误按[错误分类](errors.md#error-kinds)映射为状态码，与 JSON API 一致。`pages` 根据请求决定怎样呈现：

| 请求 | 呈现 |
| --- | --- |
| 普通请求、整页 htmx 请求 | 用 `ErrorPage` 渲染整页 |
| 局部 htmx 请求 | 不替换页面内容，错误放进 `HX-Trigger` 响应头，htmx 在 `body` 上派发 `web:flash` 事件（`event.detail.messages` 中是一条 `Type` 为 `error` 的 `web.Message`），`ui.Toaster()` 把它显示为 toast |

局部请求的错误需要别的样子时，设置 `Options.ErrorFragment` 和 `ErrorTarget`：错误用 `ErrorFragment` 渲染，通过 `HX-Retarget` 放进 `ErrorTarget` 选择的元素，例如页面顶部的一条横幅。

传给组件的 `message` 与 JSON API 的 `detail` 相同：业务错误按错误码翻译，其他错误只是状态描述，内部错误信息不会出现在页面上。5xx 错误会记录日志和[链路上的错误](telemetry.md#error-tracking)；`ui.ErrorPage` 和局部请求的提示还显示请求编号（`web.ErrorReference`，例如“请求编号：9F2K…”），用户报告问题时附上它，按它在日志中找到这个请求。自己写错误组件时同样可以调用 `web.ErrorReference(ctx)`。

htmx 4 默认会替换 4xx 和 5xx 响应，表单验证失败时返回 `422` 和带错误提示的表单即可，见[表单](forms.md#redisplaying-forms)。

中间件中需要呈现错误时，调用 `pages.Error(w, r, err)`。

<a id="redirects"></a>
## 跳转

htmx 请求由 `fetch` 发出，浏览器会自动跟随 `303` 等跳转，跳转后的整页会被当作片段放进目标元素。因此 htmx 请求的跳转要用 `HX-Redirect` 响应头，让浏览器整页跳转。

`web.Redirect` 会自动选择：htmx 请求写 `HX-Redirect`，普通请求返回 `303`：

```go
web.Redirect(w, r, "/articles")
```

在页面处理器中，直接调用 `http.Redirect` 也可以，htmx 请求的 3xx 会被改写为 `HX-Redirect`。

跳转地址来自请求时（例如 `next` 参数），用 `web.LocalPath` 确认它是本站路径，防止跳到其他站点：

```go
web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/"))
```

<a id="client-state"></a>
## 前端状态

只影响页面显示、不需要服务端参与的交互交给 Alpine。例如文章页的「目录」折叠区：

```templ
<nav x-data="{ open: false }">
	<button type="button" @click="open = !open">目录</button>
	<ol x-show="open">
		<!-- 各级标题 -->
	</ol>
</nav>
```

htmx 用 `outerMorph` 或 `innerMorph` 替换内容时，内嵌的 hx-alpine-compat 扩展会保留 Alpine 的状态：表单验证失败重新渲染后，展开的折叠区仍然是展开的。

> [!WARNING]
> htmx 的事件会冒泡。在表单上监听 `htmx:after:request` 时，表单内其他元素发出的请求也会触发它。只处理表单自己的请求时，加上 Alpine 的 `.self` 修饰符：`x-on:htmx:after:request.self="$el.reset()"`。

<a id="content-security-policy"></a>
## 内容安全策略

内容安全策略（CSP）告诉浏览器只执行带有正确 nonce 的脚本，即使页面中被注入了 `<script>`，它也不会执行。`Pages` 为每个页面响应（包括错误页）生成一个随机的 nonce，写进 `Content-Security-Policy` 响应头和 templ 的 context，默认的策略是 `web.DefaultCSP`：

```text
script-src 'self' 'nonce-…' 'unsafe-eval'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'
```

- `ui.Head()` 读取 context 中的 nonce，给它输出的 `<script>`、`<style>` 带上它；其中的 `web.Scripts()` 还设置了 htmx 的 `inlineScriptNonce`，htmx 执行交换进来的脚本时也使用这个 nonce。
- 布局中自己写的内联脚本，以及其他网站的脚本（例如[人机验证](login.md#captcha)的组件），要加上 `nonce={ templ.GetNonce(ctx) }`。
- `'self'` 允许加载本站的脚本文件，包括[静态文件](assets.md)。样式、图片、字体和 iframe 不受限制。
- `frame-ancestors 'none'` 禁止其他网站把页面嵌入框架，诱导用户点击。

页面响应同时带有 `Referrer-Policy: same-origin`，点开其他网站的链接时，不会带上本站页面的地址。

Alpine 的表达式（`x-data`、`x-on` 等）和 htmx 的 `hx-on` 在运行时编译执行，需要 `'unsafe-eval'`。去掉它之后，这两类属性都会失效。只用 htmx 属性和[界面组件](components.md)、不写 Alpine 表达式的页面可以用 `Options.CSP` 去掉它。

需要别的策略时设置 `Options.CSP`，它接收本次的 nonce，返回完整的策略；返回空字符串时不设置这个响应头。例如允许合作方的网站嵌入页面：

```go
CSP: func(nonce string) string {
    return strings.Replace(web.DefaultCSP(nonce), "frame-ancestors 'none'", "frame-ancestors https://partner.example.com", 1)
},
```

> [!WARNING]
> 不要把应用的脚本写在 `<body>` 里。用户点后退时，htmx 会重新请求整页并替换 `body`，其中的脚本会再执行一次，事件监听器就注册了两遍。应用的脚本放进静态文件，在 `<head>` 中加载：`<script defer src={ web.StaticURL(ctx, "js/app.js") }></script>`。
