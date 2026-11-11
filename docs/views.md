# 视图

- [简介](#introduction)
- [安装 templ](#installing-templ)
- [布局](#layouts)
- [装配页面](#assembling-pages)
- [注册页面路由](#page-routes)
- [登录](#login)
- [渲染页面](#rendering-pages)
- [局部更新](#partial-updates)
- [列表页](#list-pages)
- [错误页](#error-pages)
- [跳转](#redirects)
- [提示消息](#flash-messages)
- [触发事件](#triggering-events)
- [前端状态](#client-state)
- [内容安全策略](#content-security-policy)

<a id="introduction"></a>
## 简介

除了 JSON API，mygoravel 也可以在服务端渲染 HTML 页面，写法类似 Laravel 的 Blade 加 Livewire：

- [templ](https://templ.guide) 把 `.templ` 文件编译成带类型参数的 Go 函数，相当于 Blade 组件，错误在编译期就能发现；
- [htmx 4](https://four.htmx.org) 用 HTML 属性声明「点这里就请求某个地址，把返回的片段放到某个位置」，服务端只管返回 HTML；
- [Alpine.js](https://alpinejs.dev) 处理纯前端的小交互，例如折叠区、计数。

按钮、表格、下拉菜单、对话框这些现成的组件，见[界面组件](components.md)。

`web` 包负责把它们接到框架上：渲染 templ 组件、加载会话和检查登录、呈现错误、处理 htmx 的跳转，并内嵌了 htmx 与 Alpine 的脚本。

页面总是使用[会话](session.md)：用户登录后，服务器把身份记在会话里，浏览器通过 cookie 自动携带。JSON API 则只用 Bearer token，不使用会话，见[认证](authentication.md)。

本章把博客的编辑后台做成服务端渲染的页面：编辑登录后打开文章的编辑页，预览并保存修改。表单见[表单](forms.md)，实时推送见[服务端推送](sse.md)。

<a id="installing-templ"></a>
## 安装 templ

templ 的运行时库随 `web` 包引入，只需要把它的命令行工具加入 go.mod：

```bash
go get -tool github.com/a-h/templ/cmd/templ
```

修改 `.templ` 文件后运行 `go tool templ generate`，它会在旁边生成同名的 `_templ.go` 文件。生成的文件请一起提交，这样构建和 CI 不需要安装 templ。开发时可以用 `go tool templ generate --watch` 自动重新生成，编辑器插件见 [templ 文档](https://templ.guide/quick-start/installation)。

<a id="layouts"></a>
## 布局

后台的页面共用一个布局。下面是 `internal/web/layout.templ`：

```templ
package web

import "github.com/ryan053857/mygoravel/web"

templ Layout(title string) {
	<!DOCTYPE html>
	<html lang="zh-CN">
		<head>
			<meta charset="utf-8"/>
			<title>{ title }</title>
			<link rel="stylesheet" href={ web.StaticURL(ctx, "css/app.css") }/>
			@web.Scripts()
		</head>
		<body>
			<div id="flash"></div>
			<main id="main">
				{ children... }
			</main>
		</body>
	</html>
}

templ ErrorPage(status int, message string) {
	@Layout(message) {
		<h1>{ message }</h1>
	}
}

templ ErrorFragment(status int, message string) {
	<p class="error">{ message }</p>
}
```

`web.Scripts()` 输出加载 htmx、hx-sse、hx-alpine-compat 和 Alpine 的 `<script>` 标签。`web.StaticURL` 返回应用自己的样式表的地址，见[前端资源](assets.md)。`#flash` 是局部请求出错时放置错误提示的位置，见[错误页](#error-pages)。

<a id="assembling-pages"></a>
## 装配页面

在入口的 [`HTTP` 方法](dependencies.md#assembling-the-application)中创建[会话](session.md#assembling-sessions)和 `web.Pages`，再挂载内嵌脚本和 404 页面。下面是 `internal/bootstrap/http.go` 中 `HTTP` 方法的新写法：

```go
func (a *App) HTTP(d *command.Daemon) (func(context.Context) error, error) {
    sessions := session.New(a.cfg.Session, session.NewStore(a.db))
    static, err := web.NewStatic(layout.Static, web.StaticOptions{DevDir: a.cfg.StaticDir})
    if err != nil {
        return nil, err
    }
    pages := web.New(web.Options{
        Sessions:      sessions,
        Static:        static,
        Login:         "/login",
        ErrorPage:     layout.ErrorPage,
        ErrorFragment: layout.ErrorFragment,
        ErrorTarget:   "#flash",
    })

    router := chi.NewRouter()
    router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
    router.Handle(web.StaticPath+"*", static)
    router.Handle(web.AssetsPath+"*", web.Assets())
    router.NotFound(pages.NotFound)

    router.Route("/api/v1", func(r chi.Router) {
        r.Use(httpx.Timeout(a.cfg.HTTP.Timeout))
        opts := API
        opts.Router, opts.BasePath = r, "/api/v1"
        Routes(rest.New(opts), auth.Validator(a.cfg.Auth), a.db)
    })
    router.Group(func(r chi.Router) {
        r.Use(httpx.Timeout(a.cfg.HTTP.Timeout))
        PageRoutes(r, pages, a.db, a.hasher)
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
func PageRoutes(r chi.Router, pages *web.Pages, db *bun.DB, hasher *hashing.Hasher) {
    authweb.Register(r, pages, db, hasher)
    articleweb.Register(r, pages, db)
}
```

几点说明：

- 页面和 JSON API 共用一个 Router。API 挂在 `/api/v1` 下，不经过会话；`BasePath` 让 OpenAPI 文档中的地址带上这个前缀，见 [API 版本](routing.md#api-versioning)。`rest.New` 返回 JSON 格式 404 的设置只作用于这个子路由，其余地址的 404 由 `pages.NotFound` 渲染成页面。
- `static` 提供应用自己的样式表、图片等静态文件，`layout` 是应用的 `internal/web` 包，见[前端资源](assets.md)。
- `a.hasher` 是登录校验密码用的[哈希器](hashing.md#hashing-passwords)，和数据库一样在 `open` 中创建，见[依赖装配](dependencies.md#assembling-the-application)。
- `web.New` 接管会话的错误处理：会话存储出错时记录日志，并用 `ErrorPage` 呈现 `500`。
- [请求超时](middleware.md#request-timeout)按路由组安装，[服务端推送](sse.md)的路由不能有超时。

<a id="page-routes"></a>
## 注册页面路由

页面路由通过 `pages.Routes` 注册，它的第二个参数是这一组路由的访问策略：`web.Public` 允许未登录访问，`web.Require(permissions...)` 要求登录并具备权限，见[登录](#login)。下面是 `internal/article/articleweb/routes.go`：

```go
package articleweb

func Register(r chi.Router, pages *web.Pages, db *bun.DB) {
    h := &Handlers{get: article.NewGet(db), update: article.NewUpdate(db)}
    editor := pages.Routes(r, web.Require("articles"))
    editor.Get("/admin/articles/{id}/edit", h.Edit)
    editor.Put("/admin/articles/{id}", h.Update)
    editor.Post("/admin/articles/preview", h.Preview)
}
```

页面处理器的签名是 `func(w http.ResponseWriter, r *http.Request) error`。注册的每条路由会依次：拒绝跨站的非安全请求（Go 标准库的 `http.CrossOriginProtection`，htmx 发出的是同源请求，表单不需要 CSRF token，见[跨站请求伪造](session.md#csrf)）、加载和保存会话、检查访问策略，最后调用处理器，并呈现它返回的错误。

与 JSON API 的 [`rest.Public`、`rest.RequireBearer`](routing.md#access-policies) 一样，每条页面路由都必须显式声明访问策略：不经过 `pages.Routes` 就无法注册页面处理器，`web.Access` 的零值也会让注册 panic。因此不会因为漏写而意外公开一个后台页面。

[routes 命令](commands.md#routes-command)同时列出 JSON API 和页面，页面的访问策略显示为 `public` 或 `session: articles`。命令使用的 `DocRouter` 以文档模式注册全部路由，不打开数据库，页面使用 `web.ModeRoutes`，不需要会话：

```go
// DocRouter 在文档模式下构造全部路由，routes 命令用它。
func DocRouter() chi.Routes {
    router := chi.NewRouter()
    router.Route("/api/v1", func(r chi.Router) {
        opts := API
        opts.Mode, opts.Router, opts.BasePath = rest.ModeOpenAPI, r, "/api/v1"
        DocRoutes(rest.New(opts))
    })
    PageRoutes(router, web.New(web.Options{Mode: web.ModeRoutes}), nil, nil)
    return router
}
```

`PageRoutes` 收到的依赖都是 `nil`。处理器只是被登记，不会被调用，所以不受影响。

<a id="login"></a>
## 登录

`web.Require` 要求请求已经登录，并且主体能执行给出的全部权限（`sub.Can`，见[核心概念](authentication.md#key-concepts)）：

| 情况 | 结果 |
| --- | --- |
| 未登录 | 跳转到 `Options.Login`，`next` 参数带上原地址；htmx 请求通过 `HX-Redirect` 整页跳转，原地址取自发起请求的页面 |
| 缺少所需的权限 | 呈现 `403`，见[错误页](#error-pages) |
| 通过 | 主体写入请求的 context，处理器用 `identity.FromContext` 读取，再传给 action |

登录页由应用自己实现，注册为公开路由。下面是 `internal/auth/authweb/routes.go`，`user.Authenticate` 是用户模块校验密码的 action，见[哈希](hashing.md#hashing-passwords)：

```go
type LoginInput struct {
    Email    string `json:"email" format:"email"`
    Password string `json:"password" minLength:"1"`
}

func Register(r chi.Router, pages *web.Pages, db *bun.DB, hasher *hashing.Hasher) {
    h := &Handlers{pages: pages, authenticate: user.NewAuthenticate(db, hasher)}
    public := pages.Routes(r, web.Public)
    public.Get("/login", h.LoginPage)
    public.Post("/login", h.Login)
    pages.Routes(r, web.Require()).Post("/logout", h.Logout)
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) error {
    f, err := web.ParseForm[LoginInput](r)
    if err != nil {
        return err
    }
    if f.Valid() {
        u, err := h.authenticate.Handle(r.Context(), f.Data.Email, f.Data.Password)
        if err == nil {
            sub := identity.Subject{ID: u.ID, Type: identity.SubjectUser, Permissions: u.Permissions}
            if err := h.pages.Login(r, sub); err != nil {
                return err
            }
            web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/admin"))
            return nil
        }
        if !errors.Is(err, user.ErrInvalidCredentials) {
            return err
        }
        f.SetError("password", httpx.ErrorDetail(r.Context(), err))
    }
    return web.Render(w, r, http.StatusUnprocessableEntity, views.Login(f))
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) error {
    if err := h.pages.Logout(r.Context()); err != nil {
        return err
    }
    web.Redirect(w, r, "/login")
    return nil
}
```

`pages.Login` 先更换会话 token，再保存主体，防止登录前泄露的 token 在登录后继续有效；它还从请求中记录客户端地址和 User-Agent，用于[设备列表](session.md#listing-sessions)。`next` 来自请求，必须用 `web.LocalPath` 确认它是本站路径。表单的写法见[表单](forms.md)，防止密码被暴力猜测见[在处理器中限流](rate-limiting.md#in-handlers)。

会话保存的是登录时主体的快照：编辑被收回 `articles` 权限、账号被停用之后，已登录的会话仍然按旧的状态通过检查，直到重新登录。需要立即生效时设置 `Options.Resolve`，它在 `web.Require` 保护的每个请求中收到登录时保存的主体，返回本次请求的主体。它与 API 的 [`rest.Options.Resolve`](authentication.md#resolving-subjects) 签名相同，同一个函数可以交给两者：

```go
pages := web.New(web.Options{
    Sessions: sessions,
    Login:    "/login",
    Resolve: func(ctx context.Context, stored identity.Subject) (identity.Subject, error) {
        u, err := user.Find(ctx, a.db, stored.ID)
        if err != nil {
            return identity.Subject{}, err
        }
        if u.Disabled {
            return identity.Subject{}, user.ErrDisabled.New()
        }
        return identity.Subject{ID: u.ID, Type: identity.SubjectUser, Permissions: u.Permissions}, nil
    },
    // ErrorPage 等其余字段同上
})
```

| `Resolve` 返回 | 结果 |
| --- | --- |
| 主体 | 权限检查和处理器都使用它，`Method` 为 `session` |
| `errorx.Unauthenticated` 类错误（例如账号已停用），或 `ID` 为空的主体 | 销毁会话，按未登录跳转到登录页 |
| 其他 4xx 类错误（例如不是这个租户的成员） | 按分类呈现错误页，例如 `404`，不登出 |
| 5xx 类错误（例如数据库故障） | 呈现错误页 |

`user.Find` 是用户模块按 ID 查询用户的函数。每个请求因此多一次查询，访问量大时可以在 `user.Find` 中加缓存。页面同样支持[多租户](authentication.md#tenants)：`Options.Tenant` 从请求中取出租户，例如 `func(r *http.Request) string { return chi.URLParam(r, "tenant") }`，结果写入 context 供 `Resolve` 使用；登录时主体带有 `Tenant` 的会话只能在这个租户中使用。要让用户在所有设备上立即下线，而不必等到下一次请求，用 `RevokeSubject` 撤销他的全部会话，见[撤销会话](session.md#revoking-sessions)。

<a id="rendering-pages"></a>
## 渲染页面

处理器调用 action 的 `Handle`，再用 `web.Render` 渲染组件。返回的错误交给 `pages` 呈现：

```go
func (h *Handlers) Edit(w http.ResponseWriter, r *http.Request) error {
    id, err := uuid.Parse(chi.URLParam(r, "id"))
    if err != nil {
        return web.ErrNotFound.New()
    }
    sub, _ := identity.FromContext(r.Context())
    item, err := h.get.Handle(r.Context(), sub, id)
    if err != nil {
        return err // 例如 article.ErrNotFound，呈现为 404 页面
    }
    return web.Render(w, r, http.StatusOK, views.EditArticle(item))
}
```

`web.Render` 先把组件渲染到缓冲区，成功后再写出。组件用请求的 context 渲染，所以组件里可以直接用 `i18n.T(ctx, ...)` 翻译文案，见[本地化](localization.md)。templ 组件里的 `ctx` 就是这个 context。

页面和 JSON API 调用的是同一组 [action](actions.md)，业务规则和[授权](authorization.md)完全相同。

<a id="partial-updates"></a>
## 局部更新

htmx 让页面上的元素直接发请求，并把响应的 HTML 放到指定位置。例如编辑页上的「预览」按钮，把正文发给服务端渲染，结果放进 `#preview`：

```templ
<button type="button" hx-post="/admin/articles/preview" hx-include="closest form" hx-target="#preview">预览</button>
<div id="preview"></div>
```

处理器只渲染片段：

```go
func (h *Handlers) Preview(w http.ResponseWriter, r *http.Request) error {
    return web.Render(w, r, http.StatusOK, views.Preview(r.PostFormValue("content")))
}
```

同一个地址既要返回整页、又要返回片段时，用 `web.IsPartial(r)` 判断：它在 htmx 请求的目标不是整个 `body`、也没有 `hx-select` 时为真。htmx 的属性、触发方式和替换方式见 [htmx 文档](https://four.htmx.org/docs)。

<a id="list-pages"></a>
## 列表页

后台的文章列表可以搜索、按状态筛选、排序和翻页。列表的状态全部放在地址的查询参数中，例如 `/admin/articles?status=draft&sort=title&page=2`。这样刷新、后退、复制链接都能回到同一个状态。

查询参数用一个结构体描述，写法与[表单](forms.md#defining-forms)相同：字段名取 `json` 标签，规则写在 Huma 标签上。查询参数都是可选的，字段要带 `omitempty`。分页嵌入 `page.OffsetParams`。下面的 `ListQuery` 放在文章模块中，同时带上 `query` 标签，[列表接口](responses.md#offset-pagination)也可以直接使用它：

```go
type ListQuery struct {
    Q      string `query:"q" json:"q,omitempty"`
    Status string `query:"status" json:"status,omitempty" enum:"draft,published"`
    Sort   string `query:"sort" json:"sort,omitempty" default:"-created" enum:"-created,title,-title"`
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
    sub, _ := identity.FromContext(r.Context())
    items, total, err := h.list.Handle(r.Context(), sub, q)
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

`article.List` 是列表 action，用 `q.Limit()`、`q.Offset()` 查询当前页，同时返回总数。筛选、排序、翻页都只替换列表区域，`web.PushURL` 让 htmx 把地址栏改为这个状态的地址。

链接由 `web.QueryURL` 生成，它是 `ParseQuery` 的逆操作。零值和等于 `default` 的参数会被省略，同一个状态总是得到同一个地址。在视图包中写几个小函数：

```go
func listURL(q article.ListQuery) string { return web.QueryURL("/admin/articles", q) }

func pageURL(q article.ListQuery, p int) string {
    q.Page = p
    return listURL(q)
}

// sortURL 按 key 排序，已经按它升序时改为降序，并回到第一页。
func sortURL(q article.ListQuery, key string) string {
    if q.Sort == key {
        key = "-" + key
    }
    q.Sort, q.Page = key, 1
    return listURL(q)
}
```

列表区域把筛选表单也包含在内。区域每次整体替换，表单里的排序字段就总是当前的状态；搜索框带 `hx-preserve`，替换时保留原来的元素，用户可以连续输入，不会丢失焦点。htmx 4 的属性默认不继承，区域上的 `hx-target`、`hx-swap` 要加 `:inherited`，表单和链接才会沿用它们：

```templ
templ ArticleTable(q article.ListQuery, list page.Offset[article.Article]) {
	<section id="articles" hx-target:inherited="this" hx-swap:inherited="outerHTML">
		<form hx-get="/admin/articles" hx-trigger="input delay:300ms from:#q, change from:#status">
			<input id="q" name="q" type="search" value={ q.Q } hx-preserve/>
			<select id="status" name="status">
				<option value="">全部</option>
				<option value="draft" selected?={ q.Status == "draft" }>草稿</option>
				<option value="published" selected?={ q.Status == "published" }>已发布</option>
			</select>
			<input type="hidden" name="sort" value={ q.Sort }/>
		</form>
		<table>
			<tr><th><a href={ templ.SafeURL(sortURL(q, "title")) } hx-get={ sortURL(q, "title") }>标题</a></th></tr>
			for _, item := range list.Items {
				<tr><td>{ item.Title }</td></tr>
			}
		</table>
		<nav>
			for _, p := range list.Links(1) {
				if p == 0 {
					<span>…</span>
				} else {
					<a href={ templ.SafeURL(pageURL(q, p)) } hx-get={ pageURL(q, p) } aria-current?={ p == q.Page }>{ strconv.Itoa(p) }</a>
				}
			}
		</nav>
	</section>
}
```

`list.Links(1)` 返回分页导航中要显示的页码：首页、末页，以及当前页两侧各 1 页，省略的部分用 `0` 表示，例如 `[1 0 9 10 11 0 20]`。`list.LastPage()` 是最后一页的页码。链接同时写 `href`，不支持 htmx 的场景（例如在新标签页打开）也能正常访问。

两个细节：

- 表单的 `hx-trigger` 要用 `from:` 指明来源。如果写成 `input delay:300ms, change`，输入框失焦时触发的 `change` 会再提交一次，和随后点击的排序链接争抢。
- 地址交给 `web.PushURL`，元素上不写 `hx-push-url`。表单提交时会带上空字段（`?q=&status=`），由服务端推送的地址不含这些参数。

<a id="error-pages"></a>
## 错误页

处理器返回的错误按[错误分类](errors.md#error-kinds)映射为状态码，与 JSON API 一致。`pages` 根据请求决定怎样呈现：

| 请求 | 呈现 |
| --- | --- |
| 普通请求、整页 htmx 请求 | 用 `ErrorPage` 渲染整页 |
| 局部 htmx 请求 | 用 `ErrorFragment` 渲染片段，通过 `HX-Retarget` 放进 `ErrorTarget`，原来的目标保持不变 |

传给组件的 `message` 与 JSON API 的 `detail` 相同：业务错误按错误码翻译，其他错误只是状态描述，内部错误信息不会出现在页面上。5xx 错误会记录日志。

htmx 4 默认会替换 4xx 和 5xx 响应，表单验证失败时返回 `422` 和带错误提示的表单即可，见[表单](forms.md#redisplaying-forms)。

中间件中需要呈现错误时，调用 `pages.Error(w, r, err)`。

<a id="redirects"></a>
## 跳转

htmx 请求由 `fetch` 发出，浏览器会自动跟随 `303` 等跳转，跳转后的整页会被当作片段放进目标元素。因此 htmx 请求的跳转要用 `HX-Redirect` 响应头，让浏览器整页跳转。

`web.Redirect` 会自动选择：htmx 请求写 `HX-Redirect`，普通请求返回 `303`：

```go
web.Redirect(w, r, "/admin/articles")
```

在页面处理器中，直接调用 `http.Redirect` 也可以，htmx 请求的 3xx 会被改写为 `HX-Redirect`。

跳转地址来自请求时（例如登录后的 `next` 参数），用 `web.LocalPath` 确认它是本站路径，防止跳到其他站点：

```go
web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/admin"))
```

<a id="flash-messages"></a>
## 提示消息

保存文章后跳转到列表页，并在列表页提示「文章已保存」。处理器用 `web.Flash` 把提示存入会话：

```go
web.Flash(r.Context(), web.Message{Type: "success", Title: "文章已保存"})
web.Redirect(w, r, "/admin/articles")
```

`Message` 有 `Type`、`Title`、`Description` 三个字段，`Type` 惯例取 `success`、`info`、`warning`、`error`。提示只显示一次，由之后的响应负责送达：

| 之后的响应 | 提示 |
| --- | --- |
| 跳转（`303` 或 `HX-Redirect`） | 留在会话里，由跳转后的页面显示 |
| 整页 | 布局用 `web.Flashes(ctx)` 取出并渲染 |
| htmx 请求的其他响应，包括片段、`204` 和错误片段 | 放进 `HX-Trigger` 响应头，htmx 在 `body` 上派发 `web:flash` 事件，`event.detail.messages` 是提示列表 |

布局中放一个提示区域，同时处理两种情况：服务端渲染整页时取出的提示，以及 htmx 请求带来的提示。

```templ
<div id="toasts" x-data="{ messages: [] }" x-on:web:flash.window="messages.push(...$event.detail.messages)">
	for _, m := range web.Flashes(ctx) {
		<p class={ "toast", m.Type }>{ m.Title }</p>
	}
	<template x-for="m in messages">
		<p :class="'toast ' + m.type" x-text="m.title"></p>
	</template>
</div>
```

这个区域不要用作 `ErrorTarget`：错误片段会替换掉 `ErrorTarget` 的全部内容。`web.Flash` 只能在页面路由中、写出响应之前调用。

<a id="triggering-events"></a>
## 触发事件

一次请求改变的数据，常常需要更新页面上的其他区域。例如在编辑页用 htmx 保存后，侧栏的修订记录也要刷新。处理器用 `web.Trigger` 通知页面：

```go
web.Trigger(w, "article:saved", map[string]any{"id": item.ID})
```

htmx 处理完响应后，在 `body` 上派发 `article:saved` 事件，第三个参数的各项成为 `event.detail` 的字段。需要刷新的区域监听它：

```templ
<ul id="revisions" hx-get={ "/admin/articles/" + id.String() + "/revisions" } hx-trigger="article:saved from:body"></ul>
```

`web.Trigger` 可以多次调用，事件按调用顺序派发。事件派发在 `body` 上，而不是发起请求的元素上，所以即使这个元素在本次替换中被移除，事件也能送达。

<a id="client-state"></a>
## 前端状态

只影响页面显示、不需要服务端参与的交互交给 Alpine。例如编辑页的「高级选项」折叠区：

```templ
<div x-data="{ open: false }">
	<button type="button" @click="open = !open">高级选项</button>
	<div x-show="open">
		<!-- 发布时间、置顶等字段 -->
	</div>
</div>
```

htmx 用 `outerMorph` 或 `innerMorph` 替换内容时，内嵌的 hx-alpine-compat 扩展会保留 Alpine 的状态：表单验证失败重新渲染后，展开的折叠区仍然是展开的。

> [!WARNING]
> htmx 的事件会冒泡。在表单上监听 `htmx:after:request` 时，表单内其他元素发出的请求也会触发它。只处理表单自己的请求时，加上 Alpine 的 `.self` 修饰符：`x-on:htmx:after:request.self="$el.reset()"`。

<a id="content-security-policy"></a>
## 内容安全策略

内容安全策略（CSP）告诉浏览器只执行带有正确 nonce 的脚本，即使页面中被注入了 `<script>`，它也不会执行。每个请求生成一个随机的 nonce，同时写进 CSP 响应头和 templ 的 context：

```go
func csp(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        nonce := rand.Text()
        w.Header().Set("Content-Security-Policy",
            "script-src 'self' 'nonce-"+nonce+"' 'unsafe-eval'; object-src 'none'; base-uri 'none'")
        next.ServeHTTP(w, r.WithContext(templ.WithNonce(r.Context(), nonce)))
    })
}
```

`rand` 是 `crypto/rand`。在页面路由之前安装它，例如 `router.Use(csp)`。

- `web.Scripts()` 读取 context 中的 nonce，给 htmx 和 Alpine 的 `<script>` 带上它；同时设置 htmx 的 `inlineScriptNonce`，htmx 执行交换进来的脚本时也使用这个 nonce。
- 布局中自己写的内联脚本，要加上 `nonce={ templ.GetNonce(ctx) }`。
- `'self'` 允许加载本站的脚本文件，包括[静态文件](assets.md)。

Alpine 的表达式（`x-data`、`x-on` 等）和 htmx 的 `hx-on` 在运行时编译执行，需要 `'unsafe-eval'`。去掉它之后，这两类属性都会失效。只用 htmx 属性、不写 Alpine 表达式的页面可以去掉它；也可以改用 Alpine 的 [CSP 版本](https://alpinejs.dev/advanced/csp)。

> [!WARNING]
> 不要把应用的脚本写在 `<body>` 里。用户点后退时，htmx 会重新请求整页并替换 `body`，其中的脚本会再执行一次，事件监听器就注册了两遍。应用的脚本放进静态文件，在 `<head>` 中加载：`<script defer src={ web.StaticURL(ctx, "js/app.js") }></script>`。
