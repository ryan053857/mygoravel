# 服务端推送

- [简介](#introduction)
- [流式响应](#streaming-responses)
- [长连接](#long-lived-connections)
    - [断线重连](#reconnecting)
- [路由与超时](#routes-and-timeouts)
- [凭据失效](#revoked-credentials)
    - [立即断开](#immediate-revocation)
- [部署](#deployment)

<a id="introduction"></a>
## 简介

有些界面需要服务端主动推送内容：长时间任务的进度、其他人刚发布的文章、聊天消息。Tenon 使用 [Server-Sent Events](https://developer.mozilla.org/docs/Web/API/Server-sent_events)（SSE）：服务端保持一个 HTTP 响应不结束，持续写出事件，每个事件的数据是一行 JSON。

SSE 只负责服务端到客户端。客户端发给服务端的仍然是普通请求，所以认证、验证和限流都和其他接口一样。需要在线成员、「正在输入」或移动端 SDK 时，使用基于 WebSocket 的[实时频道](realtime.md)。

`rest.RegisterSSE` 注册事件流接口。它的参数和 `rest.Register` 相同，多一个事件表：键是事件名，值是事件数据的类型，用于生成 OpenAPI 文档和[客户端代码](clients.md#event-streams)。连接由 `httpx.SSE` 管理：心跳、超时、停机和断线重连。有两种用法：

| 用法 | 客户端 | 适合 |
| --- | --- | --- |
| 流式响应 | 普通的 POST 等请求，响应逐步到达 | 一次操作的进度 |
| 长连接 | GET 请求，界面打开期间一直保持 | 持续的通知、消息 |

<a id="streaming-responses"></a>
## 流式响应

博客的编辑后台可以批量发布草稿。每发布一篇文章都要给订阅者发送邮件（见[邮件](mail.md)），整批可能需要几十秒。用流式响应，每发布完一篇就推送一条进度。`publish` 是 `article.NewPublish(db)`：

```go
type PublishBatchRequest struct {
    Body struct {
        IDs []string `json:"ids" minItems:"1"`
    }
}

type PublishProgress struct {
    ID     string `json:"id"`
    Result string `json:"result"`
}

rest.RegisterSSE(api, op, map[string]any{"progress": PublishProgress{}},
    func(ctx context.Context, in *PublishBatchRequest) (*huma.StreamResponse, error) {
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
                if _, err := publish.Handle(s.Context(), sub, id); err != nil {
                    p.Result = httpx.ErrorDetail(s.Context(), err)
                }
                if err := s.SendJSON(httpx.Event{Name: "progress"}, p); err != nil {
                    return nil // 客户端已经离开
                }
            }
            return nil
        }), nil
    })
```

处理器先完成检查，出错时照常返回错误；返回 `rest.SSE` 之后才开始推送，之后返回的错误只会记录日志。`SendJSON` 把值编码为一行 JSON，`Send` 发送原样的文本。

`s.Context()` 在客户端断开或服务器开始停机时取消，传给 action，让它们随之停止。

浏览器的 `EventSource` 只能发 GET 请求，也不能带 `Authorization` 请求头。单页应用用 `fetch` 读取响应流，见[客户端代码的事件流](clients.md#event-streams)。

<a id="long-lived-connections"></a>
## 长连接

编辑后台的每个页面都显示「其他编辑刚刚发布了什么」。界面打开时连接一个事件流接口，处理器订阅[广播](broadcasting.md)的 `articles` 主题，收到一条就推送一条，直到 `s.Done()` 关闭：

```go
rest.RegisterSSE(api, op, map[string]any{"published": ArticlePublished{}},
    func(ctx context.Context, in *struct{}) (*huma.StreamResponse, error) {
        return rest.SSE(func(s *httpx.Stream) error {
            sub := hub.Subscribe("articles")
            defer sub.Close()
            for {
                select {
                case <-s.Done():
                    return nil
                case m, ok := <-sub.C():
                    if !ok {
                        return nil // 订阅已结束，客户端会重连
                    }
                    var ev ArticlePublished
                    if err := json.Unmarshal(m.Data, &ev); err != nil {
                        return err
                    }
                    if err := s.SendJSON(httpx.Event{Name: "published", ID: ev.ID.String()}, ev); err != nil {
                        return nil
                    }
                }
            }
        }), nil
    })
```

`hub` 是 `broadcast.Hub`。发布文章的请求可能在另一个实例上，Hub 把事件送到每个实例，见[广播](broadcasting.md)。

`httpx.SSE` 每 15 秒发送一次心跳，让代理不会因为连接空闲而断开它，也能尽早发现已经断开的客户端。

<a id="reconnecting"></a>
### 断线重连

连接断开后（例如网络切换、实例重启），客户端重新连接。上面的处理器为每条消息带上了 ID，客户端重连时把最后收到的 ID 放在 `Last-Event-ID` 请求头中发回，`s.LastEventID()` 返回它，处理器据此从应用的数据中补发断线期间的消息，见[断线补发](broadcasting.md#replaying-missed-events)。

<a id="routes-and-timeouts"></a>
## 路由与超时

SSE 接口和其他接口注册在同一个 API 上，认证方式相同，例如 `rest.RequireBearer(api, tokens.Validate, "articles")`。

开始推送之后，连接不再受请求时限的约束：

- [`httpx.Timeout`](middleware.md#request-timeout) 的超时不再取消 `s.Context()`，它只在客户端断开或服务器停机时取消。
- 服务器配置中的 `http.write_timeout` 同样会切断长连接，SSE 会为当前请求取消它，不需要修改配置。

停机时，`httpx.Serve` 一开始排空，`s.Done()` 就会关闭，处理器返回，连接结束，客户端随后重连到其他实例。所以打开着的长连接不会让停机等满 `shutdown_timeout`，见[平滑停止](lifecycle.md#graceful-shutdown)。

<a id="revoked-credentials"></a>
## 凭据失效

长连接只在建立时经过认证。之后 API token 被[撤销](api-tokens.md#revoking-tokens)、用户被停用或失去所需的权限，已经建立的连接也不应该继续收到消息。

`rest.RequireBearer` 在认证通过后，会在请求中登记一个重新校验函数。SSE 每 30 秒运行一次：

| 校验结果 | 连接 |
| --- | --- |
| 仍然有效 | 保持 |
| token 校验不通过、主体变了、Resolver 返回 4xx 类错误、缺少所需的 scope | 结束 |
| 暂时无法判断，例如数据库故障、Resolver 返回 5xx 类错误 | 保持，30 秒后再查 |

连接结束后，客户端重连时按普通请求重新认证，得到 `401`。所以凭据失效后，最多 30 秒连接就会断开，处理器不需要自己检查。

复查会重新运行 token 校验器和 [`Options.Accounts`](authentication.md#resolving-subjects) 中的 Resolver。校验器有副作用时（例如 `apitoken` 延长登录 token 的有效期），长连接期间也会每 30 秒发生一次。校验器可以用 `identity.Rechecking(ctx)` 区分复查和新的请求：[JWT 模式](api-tokens.md#jwt)平时只验签名，复查时查询登录是否已被撤销。自己实现认证中间件时，用 `httpx.WithCredentialCheck` 登记同样的校验函数，SSE 会照样运行它。

<a id="immediate-revocation"></a>
### 立即断开

30 秒对大多数场景已经足够。需要撤销后立即断开时，用[广播](broadcasting.md)提醒所有实例上的长连接马上校验。在[入口](dependencies.md#assembling-the-application)中基于 Hub 创建 `broadcast.Credentials`，交给 API：

```go
creds := broadcast.NewCredentials(hub)
accounts := identity.Accounts{user.Type: roles.Resolve}
api := rest.New(rest.Options{Accounts: accounts, Credentials: creds})
```

凭据变化之后，调用对应的方法：

| 方法 | 立即校验的连接 | 常见用途 |
| --- | --- | --- |
| `RecheckCredential(ctx, id)` | 使用这个凭据的连接，`id` 是主体的 `Credential`，即 token 的 ID | 退出某一台设备 |
| `RecheckSubject(ctx, ref)` | 主体 `ref`（`identity.Ref`）的全部连接 | 退出其他设备、修改密码、停用账号、调整角色、移出租户 |
| `RecheckTenant(ctx, tenant)` | 请求所在租户为 `tenant` 的全部连接 | 修改角色的权限 |

例如设备列表中的“退出这台设备”，`a.creds` 由 action 的构造函数传入：

```go
if err := a.tokens.Revoke(ctx, subject, id); err != nil {
    return err
}
if err := a.creds.RecheckCredential(ctx, id); err != nil {
    slog.WarnContext(ctx, "recheck credential", "error", err) // 连接仍会在 30 秒内断开
}
return nil
```

提醒只让连接立即执行一次上面的校验，是否断开仍由校验决定：凭据仍然有效的连接继续保持，多发的提醒只是多查一次。因此提醒要在修改提交之后发出，否则校验读到的还是旧数据；修改在事务中进行时，用 [AfterCommit](database.md#after-commit) 登记。提醒丢失时（例如 Redis 断线、发出之前进程退出），连接仍会在 30 秒的定期校验中断开。

Hub 经由 Redis 在实例之间传递消息，见[后端](broadcasting.md#backends)。

<a id="deployment"></a>
## 部署

- **代理缓冲**：nginx 默认会缓冲响应，SSE 响应带 `X-Accel-Buffering: no` 关闭它，不需要修改 nginx 配置。其他代理请确认它们不缓冲 `text/event-stream` 响应。
- **HTTP/2**：使用 HTTP/1.1 时，浏览器对同一个站点最多同时打开 6 个连接。每个打开的标签页都占用一条长连接，标签页多了以后，新的请求会一直等待。生产环境请在反向代理上启用 HTTP/2。
