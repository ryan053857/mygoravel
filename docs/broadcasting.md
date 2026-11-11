# 广播

- [简介](#introduction)
- [创建 Hub](#creating-the-hub)
- [发布事件](#publishing-events)
- [推送给浏览器](#pushing-to-browsers)
    - [断线补发](#replaying-missed-events)
- [选择后端](#backends)
    - [数据库](#database-backend)
    - [Redis 与 NATS](#redis-and-nats)
- [投递保证](#delivery-guarantees)

<a id="introduction"></a>
## 简介

[服务端推送](sse.md)让一个连接上的浏览器收到消息。部署多个实例时，发布消息的请求和要收到消息的浏览器往往在不同实例上：编辑 A 在实例 1 上发布了文章，编辑 B 的长连接在实例 2 上。`broadcast` 包负责把消息送到每个实例，再交给这个实例上订阅了它的连接。

它与传输方式无关：同一个 Hub 既可以推给 SSE 连接，也可以推给以后的 WebSocket 连接。消息按主题（topic）分发，例如 `articles`、`room:42`。

本章为博客的编辑后台加上「其他编辑刚刚发布了什么」的实时提示。

<a id="creating-the-hub"></a>
## 创建 Hub

每个进程创建一个 `broadcast.Hub`，所有长连接共用。在[入口](dependencies.md#assembling-the-application)的 `open` 中创建它，并登记关闭：

```go
hub := broadcast.New(broadcast.NewDatabase(db, nil), nil)
stack.MustAdd("broadcast", func(context.Context) error { return hub.Close() })
```

`broadcast.New` 的第一个参数是在实例之间传递消息的后端，见[选择后端](#backends)；第二个参数是日志器，传 `nil` 时使用 `slog.Default()`。Hub 创建后就在后台开始接收消息，`Close` 停止接收并结束全部订阅。

<a id="publishing-events"></a>
## 发布事件

文章发布成功后，[Publish](actions.md#writing-actions) action 发布一个事件，`hub` 由它的构造函数传入。事件在发布文章的事务中用 [AfterCommit](database.md#after-commit) 登记，事务提交之后才发出：

```go
type ArticlePublished struct {
    ID     uuid.UUID `json:"id"`
    Title  string    `json:"title"`
    Author string    `json:"author"`
}

// 在 Publish.Handle 的事务中：
tx.AfterCommit(func(ctx context.Context) error {
    err := a.hub.PublishJSON(ctx, "articles", ArticlePublished{ID: item.ID, Title: item.Title, Author: subject.ID})
    return errors.Wrap(err, "broadcast article published")
})
```

几点约定：

- **发布领域事件，不发布 HTML。** 每个连接收到后自己渲染：同一条消息，不同的人看到的样式、语言可能不同，以后的 WebSocket 客户端也许需要 JSON。事件应当小，大的内容只带 ID，由收到的一方查询。
- **在事务提交之后发布。** `Publish` 不参与数据库事务，提交之前发布，浏览器可能收到一条随后被回滚的通知。
- **发布失败不影响业务结果。** 通知是尽力而为的，文章已经发布成功，发布事件失败时框架记录一条 Error 日志。

主题由字母、数字和 `_ - : .` 组成，最长 200 字节。多租户应用中主题要带上租户，见[缓存、广播与其他资源](tenancy.md#other-resources)。

<a id="pushing-to-browsers"></a>
## 推送给浏览器

SSE 处理器订阅主题，收到事件就渲染并推送：

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

`sub.C()` 被关闭表示订阅结束，`sub.Err()` 返回原因，见[投递保证](#delivery-guarantees)。这时让处理器返回，连接随之结束，htmx 会自动重连。页面和路由的写法见[服务端推送](sse.md#long-lived-connections)。

一个订阅可以同时订阅多个主题，例如 `h.hub.Subscribe("articles", "comments")`。

<a id="replaying-missed-events"></a>
### 断线补发

连接断开期间的事件不会再投递。上面的处理器为每个事件带上了 ID，浏览器重连时通过 `Last-Event-ID` 发回最后收到的 ID。文章的 ID 是 UUIDv7，按时间排序，所以补发就是查询这个 ID 之后发布的文章。`h.publishedAfter` 是处理器持有的 `article.PublishedAfter` action：

```go
sub := h.hub.Subscribe("articles") // 先订阅，再查询，中间发布的文章不会漏掉
defer sub.Close()
return web.SSE(w, r, func(s *web.Stream) error {
    if last, err := uuid.Parse(s.LastEventID()); err == nil {
        missed, err := h.publishedAfter.Handle(s.Context(), last)
        if err != nil {
            return err
        }
        for _, item := range missed {
            if err := s.SendEvent(web.Event{ID: item.ID.String()}, views.Notice(noticeOf(item))); err != nil {
                return nil
            }
        }
    }
    // ……接着处理 sub.C()，和上面相同
})
```

一定要先订阅、再查询。反过来的话，查询之后、订阅之前发布的事件两边都收不到。先订阅时，同一篇文章可能既被查到、又从订阅中收到，按 ID 去重即可。

补发的数据来自应用自己的表，框架不另外保存事件历史。

<a id="backends"></a>
## 选择后端

| 后端 | 消息经由 | 适合 |
| --- | --- | --- |
| `broadcast.Memory()` | 本进程内 | 单实例部署（包括 SQLite）、开发和测试 |
| `broadcast.NewDatabase(db, logger)` | `broadcasts` 表，PostgreSQL、MySQL、SQLite 通用 | 多实例部署，不想引入其他组件 |
| `redisbroadcast.New(rdb, channel)` | Redis Pub/Sub | 已经使用 [Redis](redis.md) |
| `natsbroadcast.New(nc, prefix)` | NATS 核心发布订阅 | 已经使用 NATS，或者消息量很大 |

SQLite 只能单实例运行，用 `Memory` 即可。滚动发布时新旧进程短暂并存，连在旧进程上的浏览器会在旧进程退出时重连到新进程，并按 `Last-Event-ID` 补发。

<a id="database-backend"></a>
### 数据库

`NewDatabase` 把每条消息插入 `broadcasts` 表，每个实例每 100 毫秒读取新的行，所以消息最多延迟一个轮询间隔。使用前先发布迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "broadcast", FS: broadcast.Migrations()},
))
```

表中的行只需要保留很短的时间，`Cleanup` 删除 10 分钟以前的行，交给[定时任务](scheduling.md#framework-cleanups)定期执行：

```go
sched.Add("broadcasts:cleanup", "*/10 * * * *", schedule.Cleanup(broadcast.NewDatabase(a.db, nil).Cleanup, 1000))
```

自增 ID 的分配顺序和提交顺序不一定一致：两条并发的消息，后分配到 ID 的可能先提交。读到不连续的 ID 时，`Database` 会等待缺失的行最多 2 秒：期间提交了，就按顺序投递；超时还没出现，就视为回滚留下的空号跳过。所以 `Publish` 应当在业务事务之外调用，见[发布事件](#publishing-events)。数据库暂时不可用时，`Database` 从上次读到的位置继续，不会丢消息。

<a id="redis-and-nats"></a>
### Redis 与 NATS

两者都在子包中，只有导入的应用才会链接对应的客户端：

```go
hub := broadcast.New(redisbroadcast.New(rdb, "blog:broadcast"), nil)
hub := broadcast.New(natsbroadcast.New(nc, "blog.broadcast"), nil)
```

`rdb` 是[入口中创建的 Redis 客户端](redis.md#connecting)，`nc` 是应用用 `nats.Connect` 创建的连接，由应用负责关闭。频道名和主题前缀区分不同的应用，同一个应用的实例必须相同。

Redis Pub/Sub 和 NATS 核心发布订阅都不保存消息：订阅连接中断时，期间发布的消息就丢了。后端发现连接中断过，会结束本实例的全部订阅，让浏览器重连补发，见下一节。

<a id="delivery-guarantees"></a>
## 投递保证

消息只投递给当前在线的订阅，最多一次。Hub 宁可结束订阅，也不会悄悄丢掉消息：

| 原因 | `sub.Err()` | 发生时 |
| --- | --- | --- |
| 订阅跟不上 | `broadcast.ErrSlow` | 订阅的缓冲区（64 条）已满，例如浏览器网络很慢 |
| 后端可能丢了消息 | `broadcast.ErrLost` | 与 Redis、NATS 的连接中断过 |
| 订阅被关闭 | `broadcast.ErrClosed` | 调用了 `sub.Close()` 或 `hub.Close()` |

订阅结束后，处理器返回、连接结束，浏览器重连并按 `Last-Event-ID` [补发](#replaying-missed-events)，消息因此既不会丢，也不会乱序。一个订阅跟不上，不会影响其他订阅。

每个实例从后端收到全部主题的消息，再在本地按主题分发。消息量很大、主题很多时，用 NATS 并按业务拆分前缀。
