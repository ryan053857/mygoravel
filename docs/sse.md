# 服务端推送

- [简介](#introduction)
- [流式响应](#streaming-responses)
- [长连接](#long-lived-connections)
    - [断线重连](#reconnecting)
- [JSON API](#json-api)
- [路由与超时](#routes-and-timeouts)
- [部署](#deployment)

<a id="introduction"></a>
## 简介

有些页面需要服务端主动推送内容：长时间任务的进度、其他人刚发布的文章、聊天消息。mygoravel 使用 [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events)（SSE）：服务端保持一个 HTTP 响应不结束，持续写出事件；浏览器端由内嵌的 htmx [hx-sse 扩展](https://four.htmx.org/extensions/hx-sse)接收，把事件中的 HTML 放进页面。

SSE 只负责服务端到浏览器。浏览器发给服务端的仍然是普通请求，所以认证、跨站请求伪造防护、验证和限流都和其他页面一样。

页面使用 `web.SSE` 写出事件流，事件内容是 HTML 片段；JSON API 使用 `rest.RegisterSSE`，事件内容是 JSON，见 [JSON API](#json-api)。两者共用 `httpx.SSE` 管理连接：心跳、超时、停机和断线重连的行为都相同。

`web.SSE` 有两种用法：

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

每条消息按 `hx-target` 和 `hx-swap` 放进 `#progress`，`fn` 返回后响应结束。开始写事件之前，错误照常由 `pages` 呈现；开始之后，返回的错误只会记录日志。

`s.Context()` 在浏览器断开或服务器开始停机时取消，传给业务服务，让它们随之停止。

<a id="long-lived-connections"></a>
## 长连接

后台的每个页面都显示「其他编辑刚刚发布了什么」。布局中放一个连接元素，页面加载时打开长连接：

```templ
<div id="notices"></div>
<div hx-sse:connect="/admin/events"></div>
```

处理器订阅[广播](broadcasting.md)的 `articles` 主题，收到一条就推送一条，直到 `s.Done()` 关闭：

```go
func (h *Handlers) Events(w http.ResponseWriter, r *http.Request) error {
    sub := h.hub.Subscribe("articles")
    defer sub.Close()
    return web.SSE(w, r, func(s *web.Stream) error {
        for {
            select {
            case <-s.Done():
                return nil
            case m, ok := <-sub.C():
                if !ok {
                    return nil // 订阅已结束，浏览器会重连
                }
                var ev ArticlePublished
                if err := json.Unmarshal(m.Data, &ev); err != nil {
                    return err
                }
                if err := s.SendEvent(web.Event{ID: ev.ID.String()}, views.Notice(ev)); err != nil {
                    return nil
                }
            }
        }
    })
}
```

`h.hub` 是 `broadcast.Hub`。发布文章的请求可能在另一个实例上，Hub 把事件送到每个实例，见[广播](broadcasting.md)。

连接元素自身没有内容可替换，组件要用 `<hx-partial>` 指定位置。一条消息里可以有多个 `<hx-partial>`，一次更新页面的多处：

```templ
templ Notice(ev ArticlePublished) {
	<hx-partial hx-target="#notices" hx-swap="afterbegin">
		<p>{ ev.Author } 发布了《{ ev.Title }》</p>
	</hx-partial>
}
```

`web.SSE` 每 15 秒发送一次心跳，让代理不会因为连接空闲而断开它，也能尽早发现已经断开的浏览器。

<a id="reconnecting"></a>
### 断线重连

连接断开后（例如网络切换、实例重启），htmx 会自动重连，间隔从 0.5 秒开始逐次加倍。

上面的处理器用 `SendEvent` 为每条消息带上了 ID。浏览器重连时会把最后收到的 ID 放在 `Last-Event-ID` 请求头中发回，`s.LastEventID()` 返回它，处理器据此从应用的数据中补发断线期间的消息，见[断线补发](broadcasting.md#replaying-missed-events)。

`Event` 还可以设置 `Name`。有名字的事件不替换内容，htmx 会在页面上派发同名的 DOM 事件，可以用 `hx-trigger` 或 Alpine 的 `x-on` 监听。

<a id="json-api"></a>
## JSON API

App 或第三方调用者需要同样的进度时，用 `rest.RegisterSSE` 注册一个 JSON 事件流接口。它的参数和 `rest.Register` 相同，多一个事件表：键是事件名，值是事件数据的类型，用于生成 OpenAPI 文档：

```go
type PublishRequest struct {
    Body PublishInput
}

type PublishProgress struct {
    ID     string `json:"id"`
    Result string `json:"result"`
}

rest.RegisterSSE(api, op, map[string]any{"progress": PublishProgress{}},
    func() func(context.Context, *PublishRequest) (*huma.StreamResponse, error) {
        return func(ctx context.Context, in *PublishRequest) (*huma.StreamResponse, error) {
            ids := make([]uuid.UUID, len(in.Body.IDs))
            for i, raw := range in.Body.IDs {
                id, err := uuid.Parse(raw)
                if err != nil {
                    // 还没有开始推送，客户端照常收到 Problem Details
                    return nil, huma.Error422UnprocessableEntity("invalid article id " + raw)
                }
                ids[i] = id
            }
            sub, _ := identity.FromContext(ctx)
            return rest.SSE(func(s *httpx.Stream) error {
                for _, id := range ids {
                    p := PublishProgress{ID: id.String(), Result: "已发布"}
                    if _, err := articles.Publish(s.Context(), sub, id); err != nil {
                        p.Result = httpx.ErrorDetail(s.Context(), err)
                    }
                    if err := s.SendJSON(httpx.Event{Name: "progress"}, p); err != nil {
                        return nil // 客户端已经离开
                    }
                }
                return nil
            }), nil
        }
    })
```

处理器先完成检查，出错时照常返回错误；返回 `rest.SSE` 之后才开始推送。`httpx.Stream` 和 `web.Stream` 一样提供 `Context`、`Done` 和 `LastEventID`，`SendJSON` 把值编码为一行 JSON，`Send` 发送原样的文本。

浏览器的 `EventSource` 只能发 GET 请求。需要 POST 时，前端用 `fetch` 读取响应流，或者使用 [@microsoft/fetch-event-source](https://github.com/Azure/fetch-event-source) 这类库。

<a id="routes-and-timeouts"></a>
## 路由与超时

SSE 路由和普通页面注册在一起即可，例如直接加进 `articleweb.Register`（见[注册页面路由](views.md#page-routes)）：

```go
editor := pages.Routes(r, web.Require("articles"))
// ...
editor.Get("/admin/events", h.Events)
editor.Post("/admin/articles/publish", h.Publish)
```

开始推送之后，连接不再受请求时限的约束：

- [`httpx.Timeout`](middleware.md#request-timeout) 的超时不再取消 `s.Context()`，它只在客户端断开或服务器停机时取消。
- 服务器配置中的 `http.write_timeout` 同样会切断长连接，SSE 会为当前请求取消它，不需要修改配置。

停机时，`httpx.ListenAndServe` 一开始排空，`s.Done()` 就会关闭，处理器返回，连接结束，浏览器随后重连到其他实例。所以打开着的长连接不会让停机等满 `shutdown_timeout`，见[平滑停止](lifecycle.md#graceful-shutdown)。

<a id="deployment"></a>
## 部署

- **代理缓冲**：nginx 默认会缓冲响应，`web.SSE` 发出 `X-Accel-Buffering: no` 关闭它，不需要修改 nginx 配置。其他代理请确认它们不缓冲 `text/event-stream` 响应。
- **HTTP/2**：使用 HTTP/1.1 时，浏览器对同一个站点最多同时打开 6 个连接。每个打开的标签页都占用一条长连接，标签页多了以后，新的请求会一直等待。生产环境请在反向代理上启用 HTTP/2。
- **会话**：会话在响应开始写出时保存，`web.SSE` 开始之后对会话的修改不会生效。
