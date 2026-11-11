# 后台操作

- [简介](#introduction)
- [安装](#installation)
- [发起操作](#starting)
- [执行操作](#running)
- [并行处理](#fanout)
- [进度推送](#progress)
- [任务中心](#listing)
- [清理](#cleanup)

<a id="introduction"></a>
## 简介

有些操作要在后台跑好几分钟：导入几万行表格、导出几十万篇文章、批量发布选中的一百篇文章。用户需要看到进度，结束后知道结果，还希望在一个“任务中心”里看到自己发起过的所有操作。

`operation` 包把这些操作记在 `operations` 表中：谁发起的、是什么操作、当前状态、处理了多少、失败了多少、失败的原因。[后台导入和导出](import-export.md#background-imports)也建立在它之上，所以导入、导出和应用自己的批量操作出现在同一个列表里，用同一种方式推送进度。支持 PostgreSQL、SQLite 和 MySQL。

操作只负责记录，执行仍然是普通的[队列](queue.md)任务。

本章的例子：作者在文章列表中选中多篇草稿，一键发布。

<a id="installation"></a>
## 安装

把 `operation` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`。它要排在 `imports`、`exports` 之前，它们的表引用 `operations` 表：

```go
migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "queue", FS: queue.Migrations()},
    migrate.Source{Name: "operation", FS: operation.Migrations()},
)
```

在 `bootstrap.App` 中创建服务，交给需要它的 action：

```go
ops := operation.New(db)
```

<a id="starting"></a>
## 发起操作

下面是完整的 `internal/article/bulk_publish.go`，每一篇仍然由[发布文章](actions.md#writing-actions)的 `Publish` 完成：

```go
package article

import (
    "context"

    "github.com/cockroachdb/errors"
    "github.com/danielgtaylor/huma/v2"
    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/broadcast"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/errorx"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/notification"
    "github.com/ryan053857/mygoravel/operation"
    "github.com/ryan053857/mygoravel/queue"
)

// BulkPublishRequest 是批量发布的任务：哪一个操作、谁发起的、发布哪些文章。
type BulkPublishRequest struct {
    Operation uuid.UUID        `json:"operation"`
    Subject   identity.Subject `json:"subject"`
    IDs       []uuid.UUID      `json:"ids"`
}

// BulkPublishJob 是批量发布文章的任务。
var BulkPublishJob = queue.Topic[BulkPublishRequest]("articles.bulk_publish")

// BulkPublish 在后台发布多篇文章。
type BulkPublish struct {
    db      *bun.DB
    hub     *broadcast.Hub
    ops     *operation.Service
    publish *Publish
}

func NewBulkPublish(db *bun.DB, hub *broadcast.Hub, ops *operation.Service, publish *Publish) *BulkPublish {
    return &BulkPublish{db: db, hub: hub, ops: ops, publish: publish}
}

// Handle 登记操作并发布任务。
func (a *BulkPublish) Handle(ctx context.Context, subject identity.Subject, ids []uuid.UUID) (*operation.Operation, error) {
    var op *operation.Operation
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        var err error
        if op, err = a.ops.Start(ctx, tx, subject, "articles.bulk_publish"); err != nil {
            return err
        }
        return BulkPublishJob.Publish(ctx, queue.Database(tx), BulkPublishRequest{Operation: op.ID, Subject: subject, IDs: ids})
    })
    return op, err
}

type BulkPublishInput struct {
    Body struct {
        IDs []uuid.UUID `json:"ids" minItems:"1" maxItems:"1000"`
    }
}

// OperationProgress 是操作的进度，ErrorMessage 是操作失败的原因。
type OperationProgress struct {
    *operation.Operation
    ErrorMessage string `json:"error_message,omitempty"`
}

func operationProgressOf(ctx context.Context, op *operation.Operation) OperationProgress {
    return OperationProgress{Operation: op, ErrorMessage: op.ErrorMessage(ctx)}
}

type OperationOutput struct {
    Body OperationProgress
}

// HTTP 处理 POST /articles/bulk-publish，返回操作，前端用它的 ID 查看进度。
func (a *BulkPublish) HTTP(ctx context.Context, in *BulkPublishInput) (*OperationOutput, error) {
    subject, _ := identity.FromContext(ctx)
    op, err := a.Handle(ctx, subject, in.Body.IDs)
    if err != nil {
        return nil, err
    }
    return &OperationOutput{Body: operationProgressOf(ctx, op)}, nil
}

// Job 逐篇发布，每篇保存一次进度。
func (a *BulkPublish) Job(ctx context.Context, req BulkPublishRequest) error {
    return a.ops.Run(ctx, req.Operation, func(ctx context.Context, r *operation.Run) error {
        r.SetTotal(len(req.IDs))
        for _, id := range req.IDs[r.Operation().Processed:] { // 重试时跳过已经保存的
            _, err := a.publish.Handle(ctx, req.Subject, id)
            switch {
            case err == nil, errors.Is(err, ErrAlreadyPublished):
                r.Add(1, 0)
            case errorx.CodeOf(err) != "":
                r.Add(1, 1) // 文章已被删除、不是作者：只影响这一篇
            default:
                return err // 数据库不可用等，队列稍后重试
            }
            if err := r.Save(ctx, nil); err != nil {
                return err
            }
        }
        return nil
    }, a.saved)
}

// operationTopic 是一个操作的广播主题。每次保存进度后发布一条空消息，提醒正在查看的连接重新读取进度。
func operationTopic(id uuid.UUID) string {
    return "operation:" + id.String()
}

// saved 在每次保存进度的事务中调用：提交后广播进度；操作结束时在同一个事务中通知发起人。
func (a *BulkPublish) saved(ctx context.Context, tx database.Tx, op *operation.Operation) error {
    tx.AfterCommit(func(ctx context.Context) error {
        return a.hub.Publish(ctx, operationTopic(op.ID), nil)
    })
    if !op.Finished() {
        return nil
    }
    _, err := notification.Send(ctx, tx, notification.Notification{
        Event: "articles.bulk_published",
        Data: map[string]any{
            "operation_id": op.ID, "status": op.Status,
            "published": op.Processed - op.Failed, "failed": op.Failed,
        },
    }, notification.RecipientOf(op.Owner()))
    return err
}

type OperationIDInput struct {
    ID uuid.UUID `path:"id"`
}

// Progress 处理 GET /articles/bulk-publish/{id}/progress：连接后先推送当前进度，之后每次保存进度时推送一次，
// 操作结束后关闭连接。
func (a *BulkPublish) Progress(ctx context.Context, in *OperationIDInput) (*huma.StreamResponse, error) {
    subject, _ := identity.FromContext(ctx)
    if _, err := a.ops.Get(ctx, subject, in.ID); err != nil {
        return nil, err // 还没有开始推送，客户端照常收到 404
    }
    return rest.SSE(func(s *httpx.Stream) error {
        events := a.hub.Subscribe(operationTopic(in.ID)) // 先订阅，再查询，中间保存的进度不会漏掉
        defer events.Close()
        for {
            op, err := a.ops.Get(s.Context(), subject, in.ID)
            if err != nil {
                return err
            }
            if err := s.SendJSON(httpx.Event{Name: "progress"}, operationProgressOf(s.Context(), op)); err != nil {
                return nil // 客户端已经离开
            }
            if op.Finished() {
                return nil
            }
            select {
            case <-s.Done():
                return nil
            case _, ok := <-events.C():
                if !ok {
                    return nil // 订阅已结束，客户端重连后先收到当前进度
                }
            }
        }
    }), nil
}
```

`Start` 登记一个 `pending` 状态的操作。kind 由应用定义，写成“模块.动作”，任务中心据此显示操作的名称。操作和任务在同一个事务中写入，事务回滚时都不会留下。

任务的 payload 带着发起人：`Publish` 按它的权限检查每一篇文章。`op.Owner()` 只有 ID、类型和租户，没有权限，不能代替 payload 中的主体。

<a id="running"></a>
## 执行操作

worker 注册任务，见[启动 worker](queue.md#running-workers)：

```go
article.BulkPublishJob.Handle(router, sub, article.NewBulkPublish(a.db, a.hub, a.ops, article.NewPublish(a.db)).Job)
```

`Run` 先把操作变为 `running`，然后调用回调。进度以“单位”计算，这里一篇文章是一个单位：`SetTotal` 设置总数，`Add(processed, failed)` 记下处理了几个、其中失败了几个，`Save` 把进度保存下来。回调返回 nil 时操作变为 `completed`，没有保存的进度一起保存。

回调返回错误时：

| 错误 | 结果 |
| --- | --- |
| 带错误码的业务错误 | 操作变为 `failed`，`Error` 是错误码，任务不再重试 |
| 其他错误，例如数据库不可用 | 任务稍后重试，从上次保存的进度继续，没有保存的进度丢弃 |
| 队列的最后一次重试仍然出错，或者错误被 `queue.Permanent` 标记 | 操作变为 `failed`，`Error` 是 `operation.failed` |

批量发布中一篇文章的业务错误只影响这一篇，所以回调把它记为失败的单位，自己不返回错误；`completed` 的操作中可能有失败的单位，看 `Failed`。

重试时 `r.Operation()` 是上次保存的操作，`Processed` 就是已经处理过的篇数，回调从下一篇继续。每篇保存一次，所以已经发布的文章不会重复发布。单位很多时可以每 100 个保存一次，这时重试会再处理最多 99 个单位，要保证处理能够重复执行。

`Save` 的参数是与进度一起提交的写入，例如导入把失败的行和处理到的行号写在里面：进度和这些数据要么一起保存，要么都没有保存。结果要和完成状态一起提交时用 `r.Complete(ctx, write)` 结束操作，例如导出的下载地址。同一个操作的两次执行同时保存时（例如任务的租约过期后被另一个 worker 领取），后保存的那次返回错误，回调应当停下。

> [!NOTE]
> 每次重试都从头开始的操作，在回调开头调用 `r.Restart()` 把进度清零，例如导出要重新写出整个文件。

<a id="fanout"></a>
## 并行处理

逐篇发布在一个任务中依次执行。每个单位很慢又彼此独立时，例如为每篇文章调用模型生成摘要，可以把操作扇出为多个队列任务，由多个 worker 并行处理，全部结束后操作变为 `completed`，相当于 Laravel 的 `Bus::batch`。

topic 的 payload 是 `operation.Unit[T]`，T 是一个单位的业务数据：

```go
type SummarizeUnit struct {
    Subject identity.Subject `json:"subject"`
    ID      uuid.UUID        `json:"id"`
}

var SummarizeJob = queue.Topic[operation.Unit[SummarizeUnit]]("articles.summarize")
```

登记操作后，在同一个事务中用 `operation.Fanout` 加入单位，每个单位发布为一个任务：

```go
if op, err = a.ops.Start(ctx, tx, subject, "articles.summarize"); err != nil {
    return err
}
return operation.Fanout(ctx, tx, queue.Database(tx), op.ID, SummarizeJob, units...)
```

处理函数只处理一个单位，`operation.HandleUnit` 把它包装为任务的处理函数，记下每个单位的结果：

```go
// Job 为一篇文章生成摘要。
func (a *Summarize) Job(ctx context.Context, u operation.Unit[SummarizeUnit]) error {
    return operation.HandleUnit(a.ops, func(ctx context.Context, op *operation.Operation, u SummarizeUnit) error {
        return a.summarize(ctx, u.Subject, u.ID)
    }, a.saved)(ctx, u)
}
```

| 处理函数返回 | 单位 |
| --- | --- |
| nil | 成功，`Processed` 加一 |
| 带错误码的业务错误 | 失败，`Processed`、`Failed` 各加一，不再重试 |
| 其他错误 | 队列重试；最后一次仍然出错时失败，任务转入[失败任务表](queue.md#failed-jobs)，之后重试成功时 `Failed` 减一 |

一个单位失败不会取消其他单位。每个单位结束时都在一个事务中调用 `saved`，最后一个单位结束时 `op.Finished()` 为 `true`，在这里发送通知或发布下一步任务；操作结束之后不再调用 `saved`，重试失败任务修正计数时也不会重复通知。已经成功的单位再次投递时不会再执行。

每个单位都要锁住操作的行记下结果。单位很多又很快时，用 `Run` 在一个任务中处理更合适。

<a id="progress"></a>
## 进度推送

`Run` 每次保存进度（变为 `running`、每次 `Save`、结束）都在同一个事务中调用 `saved`。例子中的 `saved` 用 [AfterCommit](database.md#after-commit) 在提交后发布广播，提醒正在查看进度的连接；操作结束时在这个事务中发送[通知](notifications.md)，通知和状态一起提交，不会重复发送，也不会漏发。

`Progress` 用 [`rest.RegisterSSE`](sse.md#json-api) 注册，连接后每次保存进度都会收到一个 `progress` 事件：

```text
event: progress
data: {"id":"0193e7a2-7c1e-7b4a-9d2f-3c5a1e8b6f10","kind":"articles.bulk_publish","status":"running","total":100,"processed":42,"failed":1,"created_at":"2026-11-11T08:00:00Z","updated_at":"2026-11-11T08:00:07Z"}
```

前端用 `processed / total` 显示进度条，`total` 为 0 时显示“已处理 N 个”。

进度是状态而不是事件：广播只提醒连接重新读取，连接（包括断线重连）时先推送一次当前进度，所以漏掉的广播没有影响，与[通知角标](notifications.md#realtime)的做法相同。每次读取都经过 `Get`，只有发起人能看到进度，其他人收到 `operation.ErrNotFound`。

<a id="listing"></a>
## 任务中心

`List` 按时间从新到旧列出一个人发起的操作，导入、导出和批量发布都在里面：

```go
ops, err := a.ops.List(ctx, subject, operation.Query{Limit: 20})
```

`Query.Kind` 只列出一类操作；翻页时把上一页最后一个操作的 ID 传给 `Query.Before`。页面按 `Kind` 显示操作的名称，按 `Status` 显示状态，进行中的操作连接它的进度推送。

<a id="cleanup"></a>
## 清理

操作不会自动删除。用[定时任务](scheduling.md#framework-cleanups)定期删除旧的操作，导入、导出的记录和导入的失败行随之删除：

```go
sched.Add("operations:cleanup", "0 3 * * *", schedule.Cleanup(a.ops.Cleanup(30*24*time.Hour), 1000))
```
