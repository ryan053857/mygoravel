# 通知

- [简介](#introduction)
- [安装](#installation)
- [发送通知](#sending)
    - [收件人](#recipients)
    - [事件与数据](#events-and-data)
- [查看通知](#listing)
    - [标记已读](#marking-as-read)
- [同时发送邮件](#mail)
- [实时推送](#realtime)
- [保留期限](#retention)

<a id="introduction"></a>
## 简介

用户需要知道和自己有关的事：文章被发布了、被评论了、被邀请加入团队了。`notification` 包是站内通知的收件箱：每条通知是 `notifications` 表中的一行，属于某个租户中的某个主体，带有已读状态。支持 PostgreSQL、SQLite 和 MySQL。

Laravel 的通知把站内信、邮件、广播放在一个 `Notification` 类里，由 `via()` 决定发往哪些渠道。mygoravel 只提供其中缺少的收件箱，其他渠道已经有各自的包：[邮件](mail.md)、[队列](queue.md)、[广播](broadcasting.md)。一次通知发往哪些渠道，由 action 显式写出来，读代码就能看到会发生什么。

本章的例子：管理员替作者发布文章后，通知作者。

<a id="installation"></a>
## 安装

把 `notification` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`：

```go
migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "notification", FS: notification.Migrations()},
)
```

`notification` 包没有需要装配的对象，所有函数都直接接收数据库连接或事务。

<a id="sending"></a>
## 发送通知

在[发布文章](actions.md#writing-actions)的事务中调用 `notification.Send`，通知与文章状态一起提交或回滚：

```go
err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
    // ……读取、检查并更新文章，与原来相同
    if item.AuthorID == subject.ID {
        return nil // 作者自己发布的，不用通知
    }
    n := notification.Notification{
        Event: "article.published_by_admin",
        Data:  map[string]any{"article_id": item.ID, "title": item.Title, "admin_id": subject.ID},
    }
    author := notification.Recipient{Tenant: subject.Tenant, Type: identity.SubjectUser, ID: item.AuthorID}
    _, err = notification.Send(ctx, tx, n, author)
    return err
})
```

`Send` 为每个收件人写入一条通知，重复的收件人只写一次，没有收件人时什么也不做。`ID`（UUIDv7，按时间递增）和 `CreatedAt` 由 `Send` 填写。它返回写入的通知，[实时推送](#realtime)会用到。

<a id="recipients"></a>
### 收件人

`notification.Recipient` 由租户、主体类型和 ID 组成，与 `identity.Subject` 中的同名字段对应。同一个用户在两个租户中是两个收件人，各自有各自的通知，见[多租户](tenancy.md)。

读取当前登录用户的通知时，用 `notification.RecipientOf(subject)` 从主体得到收件人。

<a id="events-and-data"></a>
### 事件与数据

和[操作日志](activity.md#events-and-properties)一样，通知只保存事件和数据，显示时再翻译，修改文案、增加语言都不需要修改已有的通知：

```yaml
# locales/zh-CN.yaml
article.published_by_admin: "你的文章《{{.title}}》已由管理员发布"
```

`Data` 以 JSON 存储，读取时 UUID 是字符串，数字是 `float64`。文章之后可能改名或被删除，显示需要的内容应当在发送时写进来。

通知和操作日志可以同时写：操作日志从对象和操作人的角度记录发生过什么，通知是给收件人看的，有已读状态，收件人还可以删除它。

<a id="listing"></a>
## 查看通知

`notification.List` 按时间从新到旧返回一个收件人的通知，`UnreadCount` 返回未读数，用来在导航栏显示角标：

```go
func (h *Handlers) Index(w http.ResponseWriter, r *http.Request) error {
    sub, _ := identity.FromContext(r.Context())
    me := notification.RecipientOf(sub)
    before, _ := uuid.Parse(r.URL.Query().Get("before"))
    list, err := notification.List(r.Context(), h.db, notification.Query{Recipient: me, Before: before})
    if err != nil {
        return err
    }
    return web.Render(w, r, http.StatusOK, views.Notifications(list))
}
```

| 条件 | 含义 |
| --- | --- |
| `Unread` | 只返回未读的通知 |
| `Limit` | 返回的条数，默认 50 |
| `Before` | 翻页：传入上一页最后一条通知的 `ID`，返回比它更早的通知 |

模板中用 `i18n.T(ctx, n.Event, n.Data)` 显示每条通知，`n.Read()` 报告它是否已读。所有函数都只读写传入的收件人自己的通知，其他人的通知 ID 会被忽略或返回 `notification.ErrNotFound`，不需要另外授权。

`notification.Notification` 就是这张表的 Bun 模型，其他查询可以直接用 Bun 编写，条件请以 `tenant, recipient_type, recipient_id` 开头，与索引一致。

<a id="marking-as-read"></a>
### 标记已读

点击一条通知时，标记为已读再跳转到文章：

```go
func (h *Handlers) Open(w http.ResponseWriter, r *http.Request) error {
    id, err := uuid.Parse(chi.URLParam(r, "id"))
    if err != nil {
        return web.ErrNotFound.New()
    }
    sub, _ := identity.FromContext(r.Context())
    me := notification.RecipientOf(sub)
    n, err := notification.Get(r.Context(), h.db, me, id)
    if err != nil {
        return err
    }
    if err := notification.MarkRead(r.Context(), h.db, me, id); err != nil {
        return err
    }
    web.Redirect(w, r, "/articles/"+n.Data["article_id"].(string))
    return nil
}
```

`MarkRead` 可以一次传入多个 ID，`MarkAllRead` 把全部通知标记为已读，已经读过的通知保持原来的已读时间。`Delete` 删除通知。

<a id="mail"></a>
## 同时发送邮件

要让作者同时收到邮件，在同一个事务中把邮件交给[队列](queue.md#publishing-jobs)。邮件用作者自己的语言渲染，见[在 HTTP 之外使用](localization.md#outside-http)：

```go
_, err = notification.Send(ctx, tx, n, author)
if err != nil {
    return err
}
if !authorUser.EmailNotifications {
    return nil
}
mctx := i18n.WithLocale(ctx, a.languages, i18n.Match(a.languages, authorUser.Language))
msg, err := a.emails.Render(mctx, "article_published_by_admin", n.Data)
if err != nil {
    return err
}
msg.To = []mail.Address{{Name: authorUser.Name, Address: authorUser.Email}}
return queue.Publish(ctx, queue.Database(tx), "mail.send", msg)
```

`authorUser` 是事务中查出的作者。用户是否接收邮件这类偏好保存在应用自己的表中，由 action 判断。短信、Slack 等其他渠道也是同样的写法：实现一个发送接口，交给队列执行。

<a id="realtime"></a>
## 实时推送

让导航栏的角标在收到通知时立即更新：事务提交之后，用[广播](broadcasting.md#publishing-events)通知收件人所在的连接。`Send` 返回的通知要带出事务：

```go
var sent []notification.Notification
err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
    // ……
    sent, err = notification.Send(ctx, tx, n, author)
    return err
})
if err != nil {
    return nil, err
}
for _, n := range sent {
    if err := a.hub.PublishJSON(ctx, inboxTopic(n.Recipient()), n.ID); err != nil {
        slog.WarnContext(ctx, "broadcast notification", "error", err)
    }
}
```

```go
// inboxTopic 是一个收件人的广播主题，例如 inbox:acme:u1。
func inboxTopic(r notification.Recipient) string {
    return "inbox:" + r.Tenant + ":" + r.ID
}
```

主题只能由字母、数字和 `_ - : .` 组成，租户和用户 ID 中有其他字符时，先做转换。

页面的 SSE 处理器订阅当前用户的主题。角标是状态而不是事件：连接（包括断线重连）时先推送一次当前的未读数，之后每收到一条广播就重新查询，因此不需要按 `Last-Event-ID` 补发：

```go
func (h *Handlers) Inbox(w http.ResponseWriter, r *http.Request) error {
    sub, _ := identity.FromContext(r.Context())
    me := notification.RecipientOf(sub)
    events := h.hub.Subscribe(inboxTopic(me)) // 先订阅，再查询
    defer events.Close()
    return web.SSE(w, r, func(s *web.Stream) error {
        for {
            n, err := notification.UnreadCount(s.Context(), h.db, me)
            if err != nil {
                return err
            }
            if err := s.Send(views.UnreadBadge(n)); err != nil {
                return nil
            }
            select {
            case <-s.Done():
                return nil
            case _, ok := <-events.C():
                if !ok {
                    return nil // 订阅已结束，浏览器会重连
                }
            }
        }
    })
}
```

`views.UnreadBadge` 用 `<hx-partial>` 替换导航栏中的角标，写法见[长连接](sse.md#long-lived-connections)。标记已读之后同样发布一条广播，用户打开的其他标签页也会随之更新。

<a id="retention"></a>
## 保留期限

`notification.Cleanup` 返回删除保留期以前的通知（无论是否已读）的清理函数，交给[定时任务](scheduling.md#framework-cleanups)：

```go
sched.Add("notifications:cleanup", "0 4 * * *", schedule.Cleanup(notification.Cleanup(a.db, 90*24*time.Hour), 1000))
```
