# 服务端推送

- [简介](#introduction)
- [流式响应](#streaming-responses)
- [长连接](#long-lived-connections)
    - [断线重连](#reconnecting)
- [路由与超时](#routes-and-timeouts)
- [部署](#deployment)

<a id="introduction"></a>
## 简介

有些页面需要服务端主动推送内容：长时间任务的进度、其他人刚发布的文章、聊天消息。mygoravel 使用 [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events)（SSE）：服务端保持一个 HTTP 响应不结束，持续写出事件；浏览器端由内嵌的 htmx [hx-sse 扩展](https://four.htmx.org/extensions/hx-sse)接收，把事件中的 HTML 放进页面。

SSE 只负责服务端到浏览器。浏览器发给服务端的仍然是普通请求，所以认证、跨站请求伪造防护、验证和限流都和其他页面一样。

`web.SSE` 写出事件流，它有两种用法：

| 用法 | 浏览器端 | 适合 |
| --- | --- | --- |
| 流式响应 | 普通的 `hx-post` 等请求，响应逐步到达 | 一次操作的进度 |
| 长连接 | `hx-sse:connect`，页面打开期间一直保持 | 持续的通知、消息 |

<a id="streaming-responses"></a>
## 流式响应

博客的编辑后台可以批量发布草稿。每发布一篇文章都要给订阅者发送邮件（见[邮件](mail.md)），整批可能需要几十秒。用流式响应，每发布完一篇就显示一行：

```templ
<form hx-post="/admin/articles/publish" hx-target="#progress" hx-swap="beforeend">
	<!-- 每篇草稿一个 <input type="checkbox" name="ids" value="..."> -->
	<button>发布选中的文章</button>
</form>
<ol id="progress"></ol>
```

处理器在 `web.SSE` 中逐篇发布，每次用 `Send` 发出一个组件：

```go
type PublishInput struct {
    IDs []string `json:"ids" minItems:"1"`
}

func (h *Handlers) Publish(w http.ResponseWriter, r *http.Request) error {
    f, err := web.ParseForm[PublishInput](r)
    if err != nil {
        return err
    }
    if !f.Valid() {
        return web.Render(w, r, http.StatusUnprocessableEntity, views.Progress("请选择文章"))
    }
    sub, _ := identity.FromContext(r.Context())
    return web.SSE(w, r, func(s *web.Stream) error {
        for _, raw := range f.Data.IDs {
            line := "已发布"
            id, err := uuid.Parse(raw)
            if err == nil {
                _, err = h.articles.Publish(s.Context(), sub, id)
            }
            if err != nil {
                line = httpx.ErrorDetail(s.Context(), err)
            }
            if err := s.Send(views.Progress(raw + "：" + line)); err != nil {
                return nil // 浏览器已经离开
            }
        }
        return nil
    })
}
```

每条消息按 `hx-target` 和 `hx-swap` 放进 `#progress`，`fn` 返回后响应结束。开始写事件之前，错误照常返回给 `pages` 呈现；开始之后，返回的错误只会记录日志。

`s.Context()` 在浏览器断开或服务器开始停机时取消，传给业务服务，让它们随之停止。

<a id="long-lived-connections"></a>
## 长连接

后台的每个页面都显示「其他编辑刚刚发布了什么」。布局中放一个连接元素，页面加载时打开长连接：

```templ
<div id="notices"></div>
<div hx-sse:connect="/admin/events"></div>
```

处理器订阅通知，收到一条就推送一条，直到 `s.Done()` 关闭：

```go
func (h *Handlers) Events(w http.ResponseWriter, r *http.Request) error {
    notices, unsubscribe := h.notifier.Subscribe()
    defer unsubscribe()
    return web.SSE(w, r, func(s *web.Stream) error {
        for {
            select {
            case <-s.Done():
                return nil
            case n := <-notices:
                if err := s.Send(views.Notice(n)); err != nil {
                    return nil
                }
            }
        }
    })
}
```

`notifier` 是应用自己的进程内广播。部署多个实例时，在一个实例上发布的文章也要通知到连在其他实例上的浏览器，需要跨实例广播，例如 PostgreSQL 的 `LISTEN/NOTIFY` 或 Redis Pub/Sub。

> [!NOTE]
> 框架的跨实例广播功能还在开发中。

连接元素自身没有内容可替换，组件要用 `<hx-partial>` 指定位置。一条消息里可以有多个 `<hx-partial>`，一次更新页面的多处：

```templ
templ Notice(n Notice) {
	<hx-partial hx-target="#notices" hx-swap="afterbegin">
		<p>{ n.Author } 发布了《{ n.Title }》</p>
	</hx-partial>
}
```

`web.SSE` 每 15 秒发送一次心跳，让代理不会因为连接空闲而断开它，也能尽早发现已经断开的浏览器。

<a id="reconnecting"></a>
### 断线重连

连接断开后（例如网络切换、实例重启），htmx 会自动重连，间隔从 0.5 秒开始逐次加倍。

需要补发断线期间的消息时，用 `SendEvent` 为每条消息带上 ID。浏览器重连时会把最后收到的 ID 放在 `Last-Event-ID` 请求头中发回，`s.LastEventID()` 返回它：

```go
for _, n := range h.notifier.Since(s.LastEventID()) {
    _ = s.SendEvent(web.Event{ID: n.ID}, views.Notice(n))
}
```

`Event` 还可以设置 `Name`。有名字的事件不替换内容，htmx 会在页面上派发同名的 DOM 事件，可以用 `hx-trigger` 或 Alpine 的 `x-on` 监听。

<a id="routes-and-timeouts"></a>
## 路由与超时

长连接会一直保持，所以 SSE 路由不能安装 [`httpx.Timeout`](middleware.md#request-timeout)，否则到期就会被切断。把它们放在不安装 Timeout 的路由组中：

```go
router.Group(func(r chi.Router) {
    r.Use(httpx.Timeout(cfg.HTTP.Timeout))
    articleweb.Register(r, pages, sessions, articles)
})
router.Group(func(r chi.Router) {
    r.Use(session.RequirePage(pages, sessions, "/login"))
    r.Get("/admin/events", pages.Handle(h.Events))
    r.Post("/admin/articles/publish", pages.Handle(h.Publish))
})
```

服务器配置中的 `http.write_timeout` 同样会切断长连接，`web.SSE` 会为当前请求取消它，不需要修改配置。

停机时，`httpx.ListenAndServe` 一开始排空，`s.Done()` 就会关闭，处理器返回，连接结束，浏览器随后重连到其他实例。所以打开着的长连接不会让停机等满 `shutdown_timeout`，见[平滑停止](lifecycle.md#graceful-shutdown)。

<a id="deployment"></a>
## 部署

- **代理缓冲**：nginx 默认会缓冲响应，`web.SSE` 发出 `X-Accel-Buffering: no` 关闭它，不需要修改 nginx 配置。其他代理请确认它们不缓冲 `text/event-stream` 响应。
- **HTTP/2**：使用 HTTP/1.1 时，浏览器对同一个站点最多同时打开 6 个连接。每个打开的标签页都占用一条长连接，标签页多了以后，新的请求会一直等待。生产环境请在反向代理上启用 HTTP/2。
- **会话**：会话在响应开始写出时保存，`web.SSE` 开始之后对会话的修改不会生效。
