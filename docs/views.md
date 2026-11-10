# 视图

- [简介](#introduction)
- [安装 templ](#installing-templ)
- [布局](#layouts)
- [装配页面](#assembling-pages)
- [登录](#login)
- [渲染页面](#rendering-pages)
- [局部更新](#partial-updates)
- [错误页](#error-pages)
- [跳转](#redirects)
- [前端状态](#client-state)

<a id="introduction"></a>
## 简介

除了 JSON API，mygoravel 也可以在服务端渲染 HTML 页面，写法类似 Laravel 的 Blade 加 Livewire：

- [templ](https://templ.guide) 把 `.templ` 文件编译成带类型参数的 Go 函数，相当于 Blade 组件，错误在编译期就能发现；
- [htmx 4](https://four.htmx.org) 用 HTML 属性声明「点这里就请求某个地址，把返回的片段放到某个位置」，服务端只管返回 HTML；
- [Alpine.js](https://alpinejs.dev) 处理下拉、弹窗这类纯前端的小交互。

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

`web.Scripts()` 输出加载 htmx、hx-sse、hx-alpine-compat 和 Alpine 的 `<script>` 标签。`#flash` 是局部请求出错时放置错误提示的位置，见[错误页](#error-pages)。

<a id="assembling-pages"></a>
## 装配页面

在入口的 [`HTTP` 方法](dependencies.md#assembling-the-application)中创建[会话](session.md#assembling-sessions)和 `web.Pages`，再挂载内嵌脚本和 404 页面：

```go
import (
    "github.com/ryan053857/mygoravel/session"
    "github.com/ryan053857/mygoravel/web"
    layout "example.com/blog/internal/web"
)

sessions := session.New(a.cfg.Session, session.NewStore(a.db))
pages := web.New(web.Options{
    Sessions:      sessions,
    Login:         "/login",
    ErrorPage:     layout.ErrorPage,
    ErrorFragment: layout.ErrorFragment,
    ErrorTarget:   "#flash",
})

router := chi.NewRouter()
router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
router.Handle(web.AssetsPath+"*", web.Assets())
router.NotFound(pages.NotFound)

router.Route("/api", func(r chi.Router) {
    r.Use(httpx.Timeout(a.cfg.HTTP.Timeout))
    opts := API
    opts.Router = r
    Routes(rest.New(opts), auth.Validator(a.cfg.Auth), a.articles)
})

router.Group(func(r chi.Router) {
    r.Use(pages.Middleware, httpx.Timeout(a.cfg.HTTP.Timeout))
    authweb.Register(r, pages, sessions, a.users)
    articleweb.Register(r, pages, a.articles)
})
```

几点说明：

- `pages.Middleware` 是每个页面路由都需要的中间件：先拒绝跨站的非安全请求，再加载和保存会话。它使用 Go 标准库的 `http.CrossOriginProtection`，htmx 发出的是同源请求，表单不需要 CSRF token，见[跨站请求伪造](session.md#csrf)。
- 这段代码在 `internal/bootstrap/http.go` 的 `HTTP` 方法中，`API`、`Routes` 见[依赖装配](dependencies.md#assembling-the-application)。`a.users` 是应用自己的用户服务，和 `a.articles` 一样在 `open` 中构造。
- 页面和 JSON API 共用一个 Router。API 挂在 `/api` 下，不经过会话；`rest.New` 返回 JSON 格式 404 的设置只作用于这个子路由，其余地址的 404 由 `pages.NotFound` 渲染成页面。
- `web.New` 接管会话的错误处理：会话存储出错时记录日志，并用 `ErrorPage` 呈现 `500`。
- [请求超时](middleware.md#request-timeout)按路由组安装，[服务端推送](sse.md)的路由不能有超时。

<a id="login"></a>
## 登录

`pages.Require` 要求请求已经登录，并具备给出的全部 scope：

| 情况 | 结果 |
| --- | --- |
| 未登录 | 跳转到 `Options.Login`，`next` 参数带上原地址；htmx 请求通过 `HX-Redirect` 整页跳转，原地址取自发起请求的页面 |
| 缺少所需的 scope | 呈现 `403`，见[错误页](#error-pages) |
| 通过 | 主体写入请求的 context，处理器和业务服务用 `identity.FromContext` 读取 |

登录页由应用自己实现。下面是 `internal/auth/authweb/routes.go` 中的登录和登出处理器。`users` 是应用自己的用户服务，`Authenticate` 校验密码，见[哈希](hashing.md#hashing-passwords)：

```go
type LoginInput struct {
    Email    string `json:"email" format:"email"`
    Password string `json:"password" minLength:"1"`
}

func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) error {
    f, err := web.ParseForm[LoginInput](r)
    if err != nil {
        return err
    }
    if f.Valid() {
        u, err := h.users.Authenticate(r.Context(), f.Data.Email, f.Data.Password)
        if err == nil {
            sub := identity.Subject{ID: u.ID, Type: identity.SubjectUser, Scopes: u.Scopes}
            if err := session.Login(r.Context(), h.sessions, sub); err != nil {
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
    if err := session.Logout(r.Context(), h.sessions); err != nil {
        return err
    }
    web.Redirect(w, r, "/login")
    return nil
}
```

`session.Login` 先更换会话 token，再保存主体，防止登录前泄露的 token 在登录后继续有效。`next` 来自请求，必须用 `web.LocalPath` 确认它是本站路径。表单的写法见[表单](forms.md)，防止密码被暴力猜测见[在处理器中限流](rate-limiting.md#in-handlers)。

会话保存的是登录时主体的快照：编辑被收回 `articles` 权限、账号被停用之后，已登录的会话仍然按旧的状态通过检查，直到重新登录。需要立即生效时设置 `Options.Resolve`，它在 `pages.Require` 保护的每个请求中收到登录时保存的主体，返回当前的主体：

```go
pages := web.New(web.Options{
    Sessions: sessions,
    Login:    "/login",
    Resolve: func(r *http.Request, stored identity.Subject) (identity.Subject, error) {
        u, err := a.users.Get(r.Context(), stored.ID)
        if err != nil {
            return identity.Subject{}, err
        }
        if u.Disabled {
            return identity.Subject{}, user.ErrDisabled.New()
        }
        return identity.Subject{ID: u.ID, Type: identity.SubjectUser, Scopes: u.Scopes}, nil
    },
    // ErrorPage 等其余字段同上
})
```

| `Resolve` 返回 | 结果 |
| --- | --- |
| 主体 | scope 检查和处理器都使用它，`Method` 为 `session` |
| 4xx 类错误（例如账号已停用、用户不存在），或 `ID` 为空的主体 | 销毁会话，按未登录跳转到登录页 |
| 5xx 类错误（例如数据库故障） | 呈现错误页 |

每个请求因此多一次查询，访问量大时可以在 `users.Get` 中加缓存。要让用户在所有设备上立即下线，而不必等到下一次请求，用 `RevokeSubject` 撤销他的全部会话，见[撤销会话](session.md#revoking-sessions)。

<a id="rendering-pages"></a>
## 渲染页面

页面处理器的签名是 `func(w http.ResponseWriter, r *http.Request) error`，用 `pages.Handle` 适配成 chi 能用的处理器。下面是 `internal/article/articleweb/routes.go`：

```go
package articleweb

func Register(r chi.Router, pages *web.Pages, articles *article.Service) {
    h := &Handlers{articles: articles}
    r.Group(func(r chi.Router) {
        r.Use(pages.Require("articles"))
        r.Get("/admin/articles/{id}/edit", pages.Handle(h.Edit))
        r.Put("/admin/articles/{id}", pages.Handle(h.Update))
        r.Post("/admin/articles/preview", pages.Handle(h.Preview))
    })
}
```

处理器调用业务服务，再用 `web.Render` 渲染组件。返回的错误交给 `pages` 呈现：

```go
func (h *Handlers) Edit(w http.ResponseWriter, r *http.Request) error {
    id, err := uuid.Parse(chi.URLParam(r, "id"))
    if err != nil {
        return web.ErrNotFound.New()
    }
    sub, _ := identity.FromContext(r.Context())
    item, err := h.articles.Get(r.Context(), sub, id)
    if err != nil {
        return err // 例如 article.ErrNotFound，呈现为 404 页面
    }
    return web.Render(w, r, http.StatusOK, views.EditArticle(item))
}
```

`web.Render` 先把组件渲染到缓冲区，成功后再写出。组件用请求的 context 渲染，所以组件里可以直接用 `i18n.T(ctx, ...)` 翻译文案，见[本地化](localization.md)。templ 组件里的 `ctx` 就是这个 context。

业务服务和[授权](authorization.md)规则与 JSON API 完全相同，`articlehttp` 和 `articleweb` 调用的是同一个 `article.Service`。

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

在 `pages.Handle` 中，直接调用 `http.Redirect` 也可以，htmx 请求的 3xx 会被改写为 `HX-Redirect`。

跳转地址来自请求时（例如登录后的 `next` 参数），用 `web.LocalPath` 确认它是本站路径，防止跳到其他站点：

```go
web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/admin"))
```

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
