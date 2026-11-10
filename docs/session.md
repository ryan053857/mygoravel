# 会话

- [简介](#introduction)
- [配置](#configuration)
- [装配会话](#assembling-sessions)
- [读写会话数据](#session-data)
- [使用数据库存储](#database-store)
    - [撤销会话](#revoking-sessions)
    - [过期清理](#cleanup)
- [使用 Redis 存储](#redis-store)
- [跨站请求伪造](#csrf)

<a id="introduction"></a>
## 简介

HTTP 请求本身是无状态的。会话让服务器在多次请求之间记住同一个浏览器的数据：数据保存在服务端，浏览器通过 cookie 携带会话 token。

mygoravel 的会话基于 [alexedwards/scs](https://github.com/alexedwards/scs)。`session` 包只补上框架需要的部分：从配置创建 `*scs.SessionManager`，以及一个支持 PostgreSQL 和 SQLite 的 Bun 存储。读写会话数据直接使用 scs 的原生方法。

用会话登录用户、保护路由，见[会话认证](authentication.md#session-authentication)。

<a id="configuration"></a>
## 配置

把 `session.Config` 放进应用的根配置结构体，配置段为 `session`：

```go
type Config struct {
    // ...
    Session session.Config `yaml:"session"`
}
```

下面是全部配置项及其默认值，通常不需要写：

```yaml
session:
  lifetime: 24h        # 从登录起的最长有效期
  idle_timeout: 0s     # 最长空闲时间，0 表示不限制
  cookie:
    name: session
    domain: ""
    path: /
    secure: true       # 浏览器对 http://localhost 同样接受 Secure cookie
    same_site: lax     # lax、strict 或 none；none 要求 secure
    persist: true      # 关闭浏览器后保留 cookie，直到会话过期
```

会话 cookie 始终是 `HttpOnly`，前端脚本读不到它。

<a id="assembling-sessions"></a>
## 装配会话

在[入口](dependencies.md#assembling-the-application)的 `HTTP` 方法中创建会话管理器，把它的 `LoadAndSave` 中间件安装在 `httpx.Defaults` 之后：

```go
import (
    "github.com/alexedwards/scs/v2/memstore"
    "github.com/ryan053857/mygoravel/session"
)

sessions := session.New(a.cfg.Session, memstore.New(), nil)

router := chi.NewRouter()
router.Use(httpx.Defaults(a.cfg.HTTP, nil, nil)...)
router.Use(http.NewCrossOriginProtection().Handler)
router.Use(sessions.LoadAndSave)

api := rest.New(rest.Options{Router: router})
```

`session.New` 的第二个参数是会话存储。`memstore` 把会话放在进程内存中，适合开发和测试；重启后会话丢失，多实例部署时也不共享，生产环境请使用[数据库存储](#database-store)。第三个参数是日志器，传 `nil` 时使用 `slog.Default()`，存储出错时记录日志并返回统一格式的 `500`。

`LoadAndSave` 在请求开始时加载会话，在响应写出前保存修改过的会话并设置 cookie。读写会话数据和[会话认证](authentication.md#session-authentication)都依赖它。`CrossOriginProtection` 防御跨站请求伪造，见[跨站请求伪造](#csrf)。

<a id="session-data"></a>
## 读写会话数据

会话可以保存任意数据。直接使用 scs 的方法，例如在后台记住编辑最近打开的文章：

```go
sessions.Put(ctx, "admin.last_article", in.ID)

lastID := sessions.GetString(ctx, "admin.last_article")
```

`ctx` 是处理器收到的 context。`Get`、`Pop`、`Remove`、`RememberMe`、`Iterate` 等方法的说明见 [scs 文档](https://github.com/alexedwards/scs#usage)。存放自定义类型时，scs 使用 gob 编码，需要先调用 `gob.Register`。

<a id="database-store"></a>
## 使用数据库存储

生产环境把会话保存在数据库中，多个实例共享，重启后也不丢失。`session.NewStore` 接收应用的 `*bun.DB`：

```go
store := session.NewStore(a.db)
sessions := session.New(a.cfg.Session, store, nil)
```

使用前先发布 `sessions` 表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "session", FS: session.Migrations()},
))
```

表中每行是一个会话：`token` 是主键，`subject_id` 是[登录](authentication.md#login-and-logout)的主体 ID（未登录时为空），`data` 是 scs 编码后的会话数据，`expiry` 是过期时间，`revoked_at` 是撤销时间。

<a id="revoking-sessions"></a>
### 撤销会话

用户在多处登录后，可以在“账号安全”页面退出其他设备。`RevokeOthers` 撤销当前登录主体除本会话以外的全部会话，返回撤销的个数：

```go
n, err := store.RevokeOthers(ctx, sessions)
```

`ctx` 是处理器收到的 context，路由需要用 [`session.Require`](authentication.md#protecting-session-routes) 保护。修改密码后通常也调用它。

管理员封禁账号或调整权限后，用 `RevokeSubject` 撤销该用户的全部会话：

```go
n, err := store.RevokeSubject(ctx, userID)
```

被撤销的设备在下一次请求时成为未登录状态，受保护的路由返回 `401`。两者都是一条按 `subject_id` 索引执行的 `UPDATE`，不随会话总数变慢。

`Logout` 和撤销都只标记 `revoked_at`，不立即删除行：撤销时该设备可能正有请求在处理，请求结束时 scs 会再次保存会话，标记保证会话不会因此恢复。标记的行在过期后由 `Cleanup` 删除。

<a id="cleanup"></a>
### 过期清理

过期和撤销的会话不会再被读到，但行仍然留在表中。`Store` 不在后台启动清理协程，而是提供 `Cleanup`，每次最多删除 `limit` 条过期会话：

```go
n, err := store.Cleanup(ctx, 1000)
```

`Cleanup` 可以反复执行，多个实例同时执行也是安全的。

> [!NOTE]
> 定时任务功能还在开发中，完成后会提供把 `Cleanup` 注册为定时任务的方式。在此之前，可以在应用自己的定时器或命令中定期调用它，例如每小时一次。

<a id="redis-store"></a>
## 使用 Redis 存储

应用已经使用 [Redis](redis.md) 时，也可以把会话放进 Redis。安装 scs 的 [goredisstore](https://github.com/alexedwards/scs/tree/master/goredisstore)：

```bash
go get github.com/alexedwards/scs/goredisstore@latest
```

然后传入[入口中创建的客户端](redis.md#connecting) `a.rdb`：

```go
sessions := session.New(a.cfg.Session, goredisstore.New(a.rdb), nil)
```

其他代码不变。会话借助 Redis 的键过期自动清除，不需要迁移，也不需要定期清理。`RevokeOthers` 和 `RevokeSubject` 只由数据库存储提供。

<a id="csrf"></a>
## 跨站请求伪造

浏览器会在跨站请求中自动携带 cookie，因此使用会话的接口需要防御跨站请求伪造（CSRF）。Bearer token 不会被自动携带，不存在这个问题。

默认的 `same_site: lax` 已经阻止大多数跨站请求携带会话 cookie。[装配会话](#assembling-sessions)中安装的 `http.CrossOriginProtection` 来自 Go 标准库，它根据浏览器发送的 `Sec-Fetch-Site` 和 `Origin` 请求头拒绝跨站的非安全请求（`POST`、`PUT`、`DELETE` 等），不需要在前端处理 CSRF token。被拒绝的请求返回 `403`；需要放行其他站点时，用它的 `AddTrustedOrigin` 方法。
