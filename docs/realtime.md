# 实时频道

- [简介](#introduction)
- [创建 Realtime](#creating)
- [频道](#channels)
    - [私有频道](#private-channels)
- [在浏览器中订阅](#subscribing)
- [认证](#authentication)
    - [同源页面](#same-origin-pages)
    - [SPA 与移动端](#tokens)
    - [撤销后断开](#revocation)
- [在线成员](#presence)
    - [保存在线成员](#presence-stores)
- [客户端发布](#client-publish)
- [断线与重连](#reconnecting)
- [部署](#deployment)
- [指标](#metrics)

<a id="introduction"></a>
## 简介

[服务端推送](sse.md)适合服务器单向推送：通知角标、进度、AI 的流式回答。聊天、在线状态、「对方正在输入」这类场景需要双向的连接、在线成员和移动端 SDK，`realtime` 包为此在进程内嵌入 [Centrifuge](https://github.com/centrifugal/centrifuge)，提供 WebSocket 实时频道。客户端使用 Centrifuge 的[官方 SDK](https://centrifugal.dev/docs/transports/client_sdk)：浏览器的 centrifuge-js，以及 Swift、Java、Dart 等。

频道就是[广播](broadcasting.md)的主题。业务代码照常通过 `hub.PublishJSON` 发布，SSE 处理器和 WebSocket 客户端都会收到；多个实例之间由 Hub 的后端传递消息，不需要另外部署服务。

本章继续博客的编辑后台：其他编辑发布文章时实时出现提示，并显示谁正在编辑同一篇文章。

<a id="creating"></a>
## 创建 Realtime

每个进程创建一个 `realtime.Realtime`。在[入口](dependencies.md#assembling-the-application)的 `open` 中，接着 Hub 创建它：

```go
rt, err := realtime.New(hub, realtime.Options{})
if err != nil {
    return nil, err
}
stack.MustAdd("realtime", rt.Shutdown)
```

`Shutdown` 断开全部客户端，它们会重连到其他实例。它要在 Hub 关闭之前执行，所以登记在 Hub 之后。

<a id="channels"></a>
## 频道

客户端只能订阅登记过规则的频道。`realtime.Public` 是公开频道，任何连接都可以订阅：

```go
rt.Channel("articles", realtime.Public())
```

[发布文章](broadcasting.md#publishing-events)的 action 不需要修改：它发布到 `articles` 主题的事件，现在也会发给订阅了 `articles` 频道的客户端。

登记规则就是把一个主题公开给客户端。没有匹配规则的频道一律不能订阅，所以 Hub 上的其他主题不会因此暴露。消息的内容必须是 JSON。

<a id="private-channels"></a>
### 私有频道

频道名可以带 `{name}` 占位符，匹配一段字母、数字、`_` 和 `-`。`realtime.Private` 的授权函数收到主体和占位符的值，返回能否订阅：

```go
rt.Channel("article:{id}", realtime.Private(func(ctx context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
    id, err := uuid.Parse(p["id"])
    if err != nil {
        return false, nil
    }
    _, err = get.Handle(ctx, sub, id) // article.Get：能读取这篇文章的人可以订阅它的频道
    if errorx.KindOf(err) == errorx.NotFound {
        return false, nil
    }
    return err == nil, err
}))
```

没有登录的连接不能订阅私有频道。授权函数返回 5xx 类错误时，SDK 会稍后重新订阅。

授权只在订阅时进行。主体之后失去访问权限时，调用 `rt.Unsubscribe(subjectID, channel)` 取消他在所有实例上的订阅。

多租户应用的频道名要带上租户，并在授权函数中与主体的租户比较，见[缓存、广播与其他资源](tenancy.md#other-resources)：

```go
rt.Channel("t:{tenant}:articles", realtime.Private(func(_ context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
    return p["tenant"] == sub.Tenant && sub.Can("articles"), nil
}))
```

<a id="subscribing"></a>
## 在浏览器中订阅

把 [centrifuge-js](https://github.com/centrifugal/centrifuge-js) 的 `dist/centrifuge.js` 放进[静态文件](assets.md)，在布局中加载，然后连接并订阅：

```js
const centrifuge = new Centrifuge(`wss://${location.host}/realtime`);

const articles = centrifuge.newSubscription('articles');
articles.on('publication', (ctx) => {
    showNotice(ctx.data.title, ctx.data.author); // ctx.data 就是发布的 ArticlePublished
});
articles.subscribe();

centrifuge.connect();
```

订阅被拒绝时，订阅对象触发 `unsubscribed` 事件，`ctx.code` 为 103（没有权限）或 102（没有这个频道）。

<a id="authentication"></a>
## 认证

<a id="same-origin-pages"></a>
### 同源页面

服务端渲染的页面已经有[会话](views.md#login)。把处理器注册为页面路由，连接就使用登录的主体，不需要另外传递凭据：

```go
editor := pages.Routes(r, web.Require("articles"))
editor.Get("/realtime", func(w http.ResponseWriter, r *http.Request) error {
    rt.Handler().ServeHTTP(w, r)
    return nil
})
```

连接不受[请求超时](middleware.md#request-timeout)的影响。浏览器发起的连接只接受同源请求，其他来源的页面需要连接时，把它加入 `Options.TrustedOrigins`。

<a id="tokens"></a>
### SPA 与移动端

浏览器的 WebSocket 不能设置 `Authorization` 请求头，所以 SDK 在连接时传入 token，由 `Options.Authenticate` 校验，它通常就是 [API 令牌](api-tokens.md)的校验器。token 不保存权限，`Options.Resolve` 加载它，与 API 的 [`rest.Options.Resolve`](authorization.md#resolving-permissions) 是同一个函数：

```go
rt, err := realtime.New(hub, realtime.Options{
    Authenticate: tokens.Validate,
    Resolve:      roles.Resolve,
})
router.Handle("/realtime", rt.Handler())
```

```js
const centrifuge = new Centrifuge('wss://api.example.com/realtime', { token: apiToken });
```

[多租户](tenancy.md#identifying-tenants)应用还要告诉它连接属于哪个租户，`Options.Tenant` 与 API 的 `rest.Options.Tenant` 作用相同：`Resolve` 据此加载用户在这个租户中的权限，[租户频道](#private-channels)的授权函数才能与 `sub.Tenant` 比较，绑定了其他租户的 token 会被拒绝。

```go
rt, err := realtime.New(hub, realtime.Options{
    Authenticate: tokens.Validate,
    Resolve:      roles.Resolve,
    Tenant:       func(r *http.Request) string { return chi.URLParam(r, "tenant") },
})
router.Handle("/t/{tenant}/realtime", rt.Handler())
```

同源页面不需要设置它：连接经过页面路由，租户已经由 `web.Options.Tenant` 写入。

token 无效或 `Resolve` 拒绝主体时，连接以 3500 关闭，SDK 不再重连。连接期间两者每 30 秒重新运行一次，token 被撤销、用户被停用或移出租户后连接结束。既没有会话也没有 token 的连接是匿名的，只能订阅公开频道。

<a id="revocation"></a>
### 撤销后断开

连接期间每 30 秒重新校验一次凭据，与 [SSE](sse.md#revoked-credentials) 相同。需要撤销后立即断开时，把 [`broadcast.Credentials`](sse.md#immediate-revocation) 交给 `Options.Credentials`，之后退出登录、撤销 token 时照常调用 `Recheck*`，不需要为实时连接多做什么。

<a id="presence"></a>
## 在线成员

`realtime.Presence` 的频道除了授权，还记录订阅者：他们是频道的在线成员，彼此可以看到。授权函数多返回一个值，是其他成员能看到的资料：

```go
rt.Channel("editing:{article}", realtime.Presence(func(ctx context.Context, sub identity.Subject, p realtime.Params) (any, bool, error) {
    user, err := users.Get(ctx, sub.ID)
    if err != nil {
        return nil, false, err
    }
    return map[string]string{"name": user.Name}, sub.Can("articles"), nil
}))
```

编辑页面订阅这个频道，读取成员列表，并在成员加入、离开时更新：

```js
const editing = centrifuge.newSubscription(`editing:${articleId}`);
editing.on('subscribed', async () => {
    const { clients } = await editing.presence();
    render(Object.values(clients)); // 每个连接一项：{ client, user, chanInfo: { name } }
});
editing.on('join', (ctx) => add(ctx.info));
editing.on('leave', (ctx) => remove(ctx.info));
editing.subscribe();
```

加入和离开事件按人发出，不按连接：

- **多个标签页只算一次。** 一个人的第一个连接加入时发出 `join`，之后的连接不再发；最后一个连接离开后才发出 `leave`。`presence()` 返回的仍然是每个连接一项，显示时按 `user` 合并。
- **离开有宽限期。** 连接离开后，成员在列表中保留 10 秒，期间重新加入就没有任何事件。所以网络闪断后重连、刷新页面，其他人都看不到离开又加入。宽限期由 `Options.PresenceGrace` 设置，最长 30 秒，负数表示离开后立即发出 `leave`。客户端主动取消订阅同样经过宽限期；`rt.Unsubscribe` 把人移出频道，`leave` 立即发出。
- **事件可能重复。** 例如两个标签页同时打开时可能各发一次 `join`。按 `ctx.info.user` 处理，重复的事件就没有影响。
- **实例停止或崩溃时没有 `leave`。** 重连到其他实例的成员保持在线；没有重连的成员在一分钟内从列表中消失。页面应当定期调用 `presence()` 刷新，不要只依赖事件。

服务端用 `rt.Members` 查询在线成员，同一个主体的多个连接合并为一项。例如分配工作时只考虑在线的人：

```go
members, err := rt.Members(ctx, "editing:"+id.String())
// members[i].ID、members[i].Info、members[i].Connections
```

<a id="presence-stores"></a>
### 保存在线成员

在线成员默认保存在进程内存中，只适合单实例。多个实例要共用一个存储，交给 `Options.Presence`：

```go
rt, err := realtime.New(hub, realtime.Options{Presence: realtime.NewDatabasePresence(db)})
```

`NewDatabasePresence` 使用 `realtime_presence` 表，使用前先[发布](migrations.md#publishing-framework-migrations) `realtime.Migrations()`，并把 `Cleanup` 交给[定时任务](scheduling.md#framework-cleanups)。每个连接在每个 presence 频道中每 25 秒写入一次，presence 订阅很多时改用 [Redis](redis.md)：

```go
rt, err := realtime.New(hub, realtime.Options{Presence: redispresence.New(rdb, "blog:presence:")})
```

<a id="client-publish"></a>
## 客户端发布

「正在输入」这类提示不需要经过业务逻辑，可以让客户端直接发布。在规则上加 `ClientPublish()`：

```go
rt.Channel("typing:{article}", realtime.Private(canEdit).ClientPublish())
```

```js
typing.publish({ typing: true });
typing.on('publication', (ctx) => showTyping(ctx.info.user)); // ctx.info 是发布者，由服务器填写
```

只有已经订阅频道的客户端可以发布，发布者自己也会收到。这类消息只发给 WebSocket 客户端，`hub.Subscribe` 收不到。内容由客户端决定、服务器不校验，接收方只应当信任 `ctx.info`，不要把 `ctx.data` 当作可信的数据；需要保存或校验的内容仍然通过接口提交。

<a id="reconnecting"></a>
## 断线与重连

消息只发给当前在线的订阅，最多一次，不保存历史。SDK 在连接断开后自动重连并重新订阅，但断线期间的消息不会补发。所以每次订阅成功后，都从应用自己的接口取回最新的数据：

```js
articles.on('subscribed', async () => {
    const latest = await fetch('/admin/articles/recent').then((r) => r.json());
    replaceNotices(latest);
});
```

`subscribed` 在首次订阅和每次重连后都会触发，页面因此不需要区分两种情况。与 [SSE 的断线补发](broadcasting.md#replaying-missed-events)一样，数据以应用的表为准。Hub 的后端可能丢了消息时（例如与 Redis 的连接中断过），本实例的客户端会被断开并重连，走的是同一条路径。

<a id="deployment"></a>
## 部署

- 所有实例使用相同的 Hub 后端和相同的 `Options.Presence`。连接可以落在任意实例上，不需要会话保持。
- 反向代理要转发 WebSocket 的升级请求，例如 nginx 需要 `proxy_http_version 1.1`、`proxy_set_header Upgrade $http_upgrade` 和 `proxy_set_header Connection "upgrade"`。服务器每 25 秒发送一次心跳，代理的空闲超时要大于它。
- 实例之间的控制消息由 Centrifuge 编码。升级框架时如果 Centrifuge 的版本有变化，先查看它的发布说明是否要求所有实例同时升级。

只使用 `web.SSE` 的应用不会链接 Centrifuge。

<a id="metrics"></a>
## 指标

设置 `Options.MeterProvider` 后记录两个[指标](telemetry.md)：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `realtime.connections` | 仪表 | 本实例当前的连接数 |
| `realtime.subscriptions` | 仪表 | 这些连接的订阅数 |
