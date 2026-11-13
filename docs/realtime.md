# 实时频道

- [简介](#introduction)
- [创建 Realtime](#creating)
- [频道](#channels)
    - [私有频道](#private-channels)
- [在浏览器中订阅](#subscribing)
- [认证](#authentication)
    - [撤销后断开](#revocation)
- [在线成员](#presence)
    - [保存在线成员](#presence-stores)
- [客户端发布](#client-publish)
- [断线与重连](#reconnecting)
- [部署](#deployment)
    - [单独部署实时连接](#separate-process)
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

授权只在订阅时进行。主体之后失去访问权限时，调用 `rt.Unsubscribe(sub.Ref(), channel)` 取消他在所有实例上的订阅。

多租户应用的频道名要带上租户，并在授权函数中与主体的租户比较，见[缓存、广播与其他资源](tenancy.md#other-resources)：

```go
rt.Channel("t:{tenant}:articles", realtime.Private(func(_ context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
    return p["tenant"] == sub.Tenant && sub.Can("articles"), nil
}))
```

<a id="subscribing"></a>
## 在浏览器中订阅

编辑后台的单页应用安装 [centrifuge-js](https://github.com/centrifugal/centrifuge-js)（`npm install centrifuge`），带上登录 token 连接并订阅，认证见[下一节](#authentication)：

```js
import { Centrifuge } from 'centrifuge';
import { currentAccessToken } from './client'; // API 客户端，见 clients.md

const centrifuge = new Centrifuge(`wss://${location.host}/realtime`, { token: currentAccessToken() });

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

<a id="tokens"></a>
连接用 API 令牌认证。浏览器的 WebSocket 不能设置 `Authorization` 请求头，所以 SDK 在连接时传入 token，由 `Options.Authenticate` 校验，它通常就是 [API 令牌](api-tokens.md)的校验器。token 不保存权限，`Options.Accounts` 中这套账号的 Resolver 加载它，与 API 的 [`rest.Options.Accounts`](authorization.md#resolving-permissions) 相同；token 属于其他账号体系时连接被拒绝。设置了 `Authenticate` 就必须设置 `Accounts`：

```go
rt, err := realtime.New(hub, realtime.Options{
    Authenticate: tokens.Validate,
    Accounts:     identity.Accounts{user.Type: roles.Resolve},
})
router.Handle("/realtime", rt.Handler())
```

连接不受[请求超时](middleware.md#request-timeout)的影响。浏览器发起的连接只接受同源请求，其他来源的单页应用需要连接时，把它加入 `Options.TrustedOrigins`。

```js
const centrifuge = new Centrifuge('wss://api.example.com/realtime', { token: apiToken });
```

[多租户](tenancy.md#identifying-tenants)应用还要告诉它连接属于哪个租户，`Options.Tenant` 与 API 的 `rest.Options.Tenant` 作用相同：Resolver 据此加载用户在这个租户中的权限，[租户频道](#private-channels)的授权函数才能与 `sub.Tenant` 比较，绑定了其他租户的 token 会被拒绝。

```go
rt, err := realtime.New(hub, realtime.Options{
    Authenticate: tokens.Validate,
    Accounts:     identity.Accounts{user.Type: roles.Resolve},
    Tenant:       func(r *http.Request) string { return chi.URLParam(r, "tenant") },
})
router.Handle("/t/{tenant}/realtime", rt.Handler())
```

token 无效、属于其他账号体系，或 Resolver 拒绝主体时，连接以 3500 关闭，SDK 不再重连。连接期间两者每 30 秒重新运行一次，token 被撤销、用户被停用或移出租户后连接结束。没有 token 的连接是匿名的，只能订阅公开频道。

使用 [JWT 模式](api-tokens.md#jwt)时，SDK 传入 access token，并设置 `getToken`：

```js
import { currentAccessToken, refresh } from './client';

const centrifuge = new Centrifuge('wss://api.example.com/realtime', {
    token: currentAccessToken(),
    // 刷新后返回新的 access token；返回空字符串表示需要重新登录
    getToken: async () => (await refresh()) ? currentAccessToken() : '',
});
```

`currentAccessToken` 和 `refresh` 来自 [API 客户端](clients.md#creating-the-client)，后者用 refresh token 换新的 access token。断线一段时间后重连，access token 可能已经过期，这时连接返回 109（token 已过期），SDK 调用 `getToken` 换新的再连。已经建立的连接不受 access token 过期的影响：复查只检查这次登录是否已被撤销。

这个处理不限于 JWT：`Authenticate` 返回的错误匹配 `identity.ErrExpired` 时，连接返回 109；连接期间复查遇到它时以 3005 断开，SDK 重连时同样换新的 token。

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
    render(Object.values(clients)); // 每个连接一项：{ client, user, chanInfo: { name } }，user 是主体的 Ref，例如 user:42
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
// members[i].Subject（identity.Ref）、members[i].Info、members[i].Connections
```

<a id="presence-stores"></a>
### 保存在线成员

在线成员保存在所有实例共用的存储中，登记 presence 频道之前必须设置 `Options.Presence`，否则 `rt.Channel` 会 panic。只有一个实例时也一样：[不停机升级](deployment.md#zero-downtime-upgrades)时新旧进程同时运行，双方要看到同一份在线成员。在线成员保存在 [Redis](redis.md) 中：

```go
rt, err := realtime.New(hub, realtime.Options{Presence: redispresence.New(rdb, "blog:presence:")})
```

没有 presence 频道的应用不需要设置它，`rt.Members` 返回空列表。测试中用 `realtimetest.NewPresence()`，它把在线记录保存在内存中；同一个交给多个 `Realtime`，就模拟了共用存储的多个实例。

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

只使用[服务端推送](sse.md)的应用不会链接 Centrifuge。

<a id="separate-process"></a>
### 单独部署实时连接

`serve` 和 `start` 都挂载了 `/realtime`，起步阶段不需要别的安排。WebSocket 是长连接：每次升级，旧进程停止时断开它上面的全部连接，客户端在同一时刻重连。接口发布频繁、连接很多时，把实时连接交给单独的一组 `realtime` 进程，升级接口就不再打扰它们，大量空闲连接也不和接口争抢资源。

`realtime` 进程运行与 `serve` 相同的路由，只是监听另一个地址。把[装配页面](views.md#assembling-pages)中 `HTTP` 方法的内容移到 `serveOn`，两个部分共用：

```go
// HTTP 运行全部路由，serve 和 start 命令使用它。
func (a *App) HTTP(d *command.Daemon) (func(context.Context) error, error) {
    return a.serveOn(d, a.cfg.HTTP.Addr)
}

// Realtime 运行同样的路由，专门承接实时连接，realtime 命令使用它。
func (a *App) Realtime(d *command.Daemon) (func(context.Context) error, error) {
    return a.serveOn(d, a.cfg.RealtimeAddr)
}

// serveOn 装配路由并在 addr 上监听，就是原来 HTTP 方法的内容，只是 d.Listen("tcp", addr)。
func (a *App) serveOn(d *command.Daemon, addr string) (func(context.Context) error, error)

// Realtime 只运行实时连接，realtime 命令调用它。
func Realtime(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).Realtime)
}
```

`RealtimeAddr` 是应用配置中新增的一项，例如 `:8081`。路由相同，所以 token 照常认证。反向代理把 `/realtime` 转给 `realtime` 进程，其余请求仍然转给 `serve`：

```nginx
location /realtime {
    proxy_pass http://127.0.0.1:8081;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
}
location / {
    proxy_pass http://127.0.0.1:8080;
}
```

- 只改了接口和页面时，[升级](deployment.md#zero-downtime-upgrades) `serve` 即可，实时连接不受影响；改了频道规则、授权函数，或者升级了框架，再升级 `realtime`。
- `start` 不包含 `Realtime`，因为 `HTTP` 已经挂载了实时频道。
- `realtime` 进程同样可以运行多个实例，它们与 `serve`、`work` 共用 Hub 后端，业务代码在哪个进程中发布都一样。

<a id="metrics"></a>
## 指标

设置 `Options.MeterProvider` 后记录两个[指标](telemetry.md)：

| 指标 | 类型 | 含义 |
| --- | --- | --- |
| `realtime.connections` | 仪表 | 本实例当前的连接数 |
| `realtime.subscriptions` | 仪表 | 这些连接的订阅数 |
