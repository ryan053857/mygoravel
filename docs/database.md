# 数据库：快速入门

- [简介](#introduction)
- [配置](#configuration)
    - [连接用满时](#pool-exhaustion)
- [连接数据库](#connecting)
    - [慢查询](#slow-queries)
- [定义模型](#defining-models)
    - [主键](#primary-keys)
    - [自动维护时间戳](#timestamps)
    - [软删除](#soft-deletes)
- [执行查询](#running-queries)
    - [处理“未找到”](#handling-not-found)
- [事务](#transactions)
    - [提交后执行](#after-commit)
    - [隔离级别与死锁](#isolation-and-deadlocks)
- [处理约束错误](#constraint-errors)
- [测试](#testing)

<a id="introduction"></a>
## 简介

几乎每个 API 都需要与数据库交互。mygoravel 只支持 PostgreSQL，并使用以下组件：

- [pgx](https://github.com/jackc/pgx)：PostgreSQL 驱动；
- [Bun](https://bun.uptrace.dev)：SQL 优先的 Go ORM，负责查询构建和模型映射；
- [goose](https://github.com/pressly/goose)：数据库迁移，见[迁移](migrations.md)。

`database` 包负责打开一个配置好的连接池（[pgxpool](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool)），返回原生的 `*bun.DB`。之后的查询、事务等操作都直接使用 Bun 的 API，你可以随时参考 [Bun 官方文档](https://bun.uptrace.dev/guide/)。

<a id="configuration"></a>
## 配置

数据库配置通常放在配置文件的 `database` 段中：

```yaml
database:
  dsn: ${DATABASE_URL}
  max_conns: 20
  min_conns: 0
  max_conn_lifetime: 1h
  max_conn_idle_time: 30m
  slow_query: 1s
```

| 字段 | 说明 | `DefaultConfig` 中的默认值 |
| --- | --- | --- |
| `dsn` | PostgreSQL 连接串，例如 `postgres://user:pass@localhost:5432/app?sslmode=disable` | 无，必填 |
| `max_conns` | 最大连接数 | 4 × `GOMAXPROCS` |
| `min_conns` | 空闲时也保持的连接数 | `0` |
| `max_conn_lifetime` | 连接的最长存活时间，到期后在空闲时关闭 | `1h` |
| `max_conn_idle_time` | 超过 `min_conns` 的空闲连接的最长保留时间 | `30m` |
| `slow_query` | 执行时间达到它的语句记为[慢查询](#slow-queries)；`0` 表示不记录 | `1s` |

这些字段对应 `database.Config` 结构体。把它放进应用的根配置结构体，`config.Load` 会自动填充上表中的默认值：

```go
type Config struct {
    Database database.Config `yaml:"database"`
}

cfg, err := config.Load[Config](path)
if err != nil {
    return err
}
```

`database.Config` 实现了 `Validate` 方法，`config.Load` 会自动检查 DSN 是否为空、连接池参数是否合法；DSN 的格式在打开连接时检查。

> [!TIP]
> 设置 `max_conns` 时，请考虑应用的部署副本数：所有副本的连接数之和不应超过数据库的最大连接数。

<a id="pool-exhaustion"></a>
### 连接用满时

连接都在使用中时，新的语句排队等待空闲连接。HTTP 请求不应该无限排队：过载时排队只会让所有请求一起变慢、一起超时。`database.WithAcquireTimeout` 给取连接这一步设一个时限，超过时返回 `database.ErrBusy`，响应为 `503` 并带 `Retry-After`，客户端稍后重试：

```go
ctx = database.WithAcquireTimeout(ctx, time.Second)
```

时限只作用于取连接，不影响语句本身的执行时间。队列任务和命令不设置它，排队等待连接是它们的正常情况。连接池的使用情况和超时次数见[指标](telemetry.md#database)。

<a id="connecting"></a>
## 连接数据库

使用 `database.Open` 打开连接池。它会立即 Ping 数据库确认连通，失败时自动释放已打开的资源，并返回 `database.ErrUnavailable`。这个错误属于 `errorx.Unavailable` 分类，命令因此以 `ExitUnavailable` 退出，部署脚本可以据此重试，见[退出码](commands.md#exit-codes)。下面是一个完整的连接检查函数：

```go
package storage

import (
    "context"

    "github.com/ryan053857/mygoravel/database"
)

func Check(ctx context.Context, dsn string) error {
    cfg := database.DefaultConfig()
    cfg.DSN = dsn
    db, err := database.Open(ctx, cfg)
    if err != nil {
        return err
    }
    defer db.Close()
    return db.PingContext(ctx)
}
```

`database.Open` 使用你传入的配置值，包括显式设置的零值。为 `ctx` 设置一个截止时间，可以避免数据库不可达时启动过程无限等待。

在应用中，连接池应当在入口打开**一次**，然后传给所有需要它的 [action](actions.md)，在应用退出时关闭，见[依赖装配](dependencies.md)。报告每条语句的耗时指标和链路用 `database.WithTelemetry`，见[可观测性](telemetry.md#database)。

<a id="slow-queries"></a>
### 慢查询

执行时间达到 `slow_query`（默认 1 秒）的语句记录一条 `Warn` 日志：

```text
WARN slow query component=database duration=1.284s db.operation.name=SELECT db.collection.name=articles query="SELECT \"a\".\"id\", … FROM \"articles\" AS \"a\" WHERE (\"a\".\"author_id\" = ?) ORDER BY \"a\".\"id\" DESC LIMIT ?"
```

`query` 中的字符串、数字等字面量都换成了 `?`，日志不会带出参数的值，同一条语句不同参数的执行看起来一样，便于汇总。日志在执行语句的 `ctx` 中记录，带着请求 ID 和 `trace_id`，可以找到是哪个请求或任务执行的。日志器默认是 `slog.Default()`，用 `database.WithLogger` 指定其他的。

<a id="defining-models"></a>
## 定义模型

模型是一个带有 `bun` 标签的普通 Go 结构体，描述数据表的列与字段之间的映射。本文档以一个博客的文章为例：

```go
package article

import (
    "time"

    "github.com/google/uuid"
)

const (
    StatusDraft     = "draft"
    StatusPublished = "published"
)

type Article struct {
    ID          uuid.UUID  `bun:"id,pk"`
    AuthorID    string     `bun:"author_id,notnull"`
    Title       string     `bun:"title,notnull"`
    Slug        string     `bun:"slug,notnull"`
    Content     string     `bun:"content,notnull"`
    Status      string     `bun:"status,notnull"`
    PublishedAt *time.Time `bun:"published_at"`
    CreatedAt   time.Time  `bun:"created_at,notnull,skipupdate"`
    UpdatedAt   time.Time  `bun:"updated_at,notnull"`
    DeletedAt   time.Time  `bun:"deleted_at,soft_delete,nullzero"`
}
```

`Slug` 是文章在网址中的标识，例如 `/blog/hello-world` 中的 `hello-world`，它在所有文章中必须唯一。文章创建时是草稿（`draft`），发布后变为 `published`，并记录发布时间。

模型声明字段映射和下面介绍的 Bun 行为，表结构本身由[迁移](migrations.md#migration-structure)管理。mygoravel 不会统一添加 `created_at`、`updated_at`、`deleted_at`，你可以按需在模型和迁移中声明它们。本文的 `Article` 示例同时使用时间戳和软删除。

<a id="primary-keys"></a>
### 主键

mygoravel 约定主键使用 UUIDv7，由应用在插入前生成，迁移中声明为 `id uuid PRIMARY KEY`：

```go
item := &Article{ID: uuid.Must(uuid.NewV7()), AuthorID: subject.ID}
```

在插入之前就有 ID，同一事务中的关联记录、对象存储的键或者幂等重试都可以直接使用它。UUIDv7 以毫秒时间戳开头，新记录总是追加在索引末尾，写入性能接近自增 ID；对外暴露时也不会泄露记录数量或被逐个遍历，但它包含创建时间，也不能代替[授权](authorization.md)检查。只在内部使用、不出现在接口中的大表，可以在迁移中改用自增主键 `bigint GENERATED ALWAYS AS IDENTITY`。

<a id="timestamps"></a>
### 自动维护时间戳

如果你希望创建、修改记录时自动维护时间戳，可以在模型上实现 Bun 的 [`BeforeAppendModel`](https://bun.uptrace.dev/guide/hooks.html#beforeappendmodel) 钩子。Bun 会在构建插入或更新语句前调用它，无需在 `database.Open` 中注册。

在 `article` 包中新增 `model_hooks.go`，为上面的 `Article` 实现钩子：

```go
package article

import (
    "context"
    "time"

    "github.com/uptrace/bun"
)

var _ bun.BeforeAppendModelHook = (*Article)(nil)

func (a *Article) BeforeAppendModel(ctx context.Context, query bun.Query) error {
    now := time.Now().UTC()
    switch query.(type) {
    case *bun.InsertQuery:
        if a.CreatedAt.IsZero() {
            a.CreatedAt = now
        }
        a.UpdatedAt = now
    case *bun.UpdateQuery:
        a.UpdatedAt = now
    }
    return nil
}
```

这个实现会在插入时填充空的 `CreatedAt` 并设置 `UpdatedAt`，在更新时刷新 `UpdatedAt`。模型中 `CreatedAt` 的 `skipupdate` 标签会让 Bun 在根据模型生成更新字段时跳过创建时间。

钩子修改的是模型字段。使用 `Column(...)` 限定更新列时，仍需把 `updated_at` 包含进去。下面的 `item` 是已加载并修改标题的 `Article`，`db` 是打开的连接池：

```go
_, err := db.NewUpdate().
    Model(&item).
    Column("title", "updated_at").
    WherePK().
    Exec(ctx)
```

如果使用 `Set(...)` 显式指定更新表达式，需要同时设置 `Set("updated_at = ?", time.Now().UTC())`。`Model((*Article)(nil))` 没有模型实例可供此钩子修改，原生 SQL 也不会调用模型钩子；这些写入路径需要自行维护时间戳。迁移中的时间列不设默认值，时间总是由应用写入，这样测试才能用假时钟控制时间，见[控制时间](testing.md#controlling-time)。

<a id="soft-deletes"></a>
### 软删除

如果删除后需要保留记录，可以使用 Bun 原生的[模型软删除](https://bun.uptrace.dev/guide/soft-deletes.html#using-bun-models)。上面 `DeletedAt` 的 `soft_delete` 标签启用该功能，`nullzero` 将 Go 的零时间映射为 SQL `NULL`；迁移中对应的 `deleted_at` 列应允许为空。

对于配置了该标签的模型，`NewDelete()` 会更新 `deleted_at`，普通模型查询会自动排除已软删除的记录。下面的 `id` 是目标文章的 UUID，`ctx` 是请求上下文，`db` 是打开的连接池：

```go
// 标记为已删除，保留记录。
_, err := db.NewDelete().
    Model((*Article)(nil)).
    Where("id = ?", id).
    Exec(ctx)

// 仅查询已软删除的文章。
var deleted []Article
err = db.NewSelect().Model(&deleted).WhereDeleted().Scan(ctx)

// 查询全部文章，包括已软删除的记录。
var all []Article
err = db.NewSelect().Model(&all).WhereAllWithDeleted().Scan(ctx)

// 物理删除目标文章，无论它是否已软删除。
_, err = db.NewDelete().
    Model((*Article)(nil)).
    Where("id = ?", id).
    ForceDelete().
    Exec(ctx)
```

软删除由 Bun 根据模型标签处理，直接执行原生 SQL 时需要自行处理 `deleted_at` 条件。上述时间戳钩子只处理插入和更新查询，`NewDelete()` 的软删除不会通过其中的 `*bun.UpdateQuery` 分支刷新 `updated_at`。

软删除的记录仍然参与数据库唯一约束。本例的 `articles_slug_key` 会继续保留已删除文章的 slug；如果业务允许复用，可以在迁移中把它改为只约束未删除记录的部分唯一索引：

```sql
CREATE UNIQUE INDEX articles_slug_key ON articles (slug) WHERE deleted_at IS NULL;
```

索引沿用原来的名字，[处理约束错误](#constraint-errors)的代码不需要修改。

<a id="running-queries"></a>
## 执行查询

在 action 中，使用注入的 `db` 构建查询：

```go
var item Article
err := db.NewSelect().
    Model(&item).
    Where("id = ?", id).
    Scan(ctx)
```

查询参数通过 `?` 占位符绑定，Bun 会负责转义，请不要手动拼接 SQL 字符串。始终把请求的 `ctx` 传给 `Scan` 或 `Exec`，这样请求取消或超时时，查询也会随之停止。

插入、更新和删除同样使用 Bun 的原生 API：

```go
_, err := db.NewInsert().Model(&item).Exec(ctx)
_, err = db.NewUpdate().Model(&item).WherePK().Exec(ctx)
_, err = db.NewDelete().Model((*Article)(nil)).Where("id = ?", id).Exec(ctx)
```

<a id="handling-not-found"></a>
### 处理“未找到”

查询单条记录时，如果没有匹配的行，Bun 会返回 `sql.ErrNoRows`。请把它转换为业务的“未找到”错误；其他数据库错误用 `cockroachdb/errors` 的 `errors.Wrap` 补充上下文后返回：

```go
if errors.Is(err, sql.ErrNoRows) {
    return nil, ErrNotFound.New()
}
if err != nil {
    return nil, errors.Wrap(err, "load article")
}
```

其他数据库错误会被当作内部错误，返回 `500`，完整的错误链和堆栈会记录到日志中，见[错误处理](errors.md#wrapping-errors)。

<a id="transactions"></a>
## 事务

使用 `database.RunInTx` 在事务中执行一组操作。回调返回错误时事务回滚，返回 `nil` 时提交：

```go
err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
    if _, err := tx.NewInsert().Model(item).Exec(ctx); err != nil {
        return err
    }
    _, err := tx.NewInsert().Model(audit).Exec(ctx)
    return err
})
```

参数与 Bun 的 `db.RunInTx` 相同，只是数据库作为第二个参数传入；第三个参数是 `*sql.TxOptions`，`nil` 表示默认选项。`database.Tx` 嵌入了 `bun.Tx`，查询的写法完全一样，也可以传给接收 `bun.IDB` 的函数，例如 `activity.Record` 和队列的 `Topic.Publish`。它比 `bun.Tx` 多了一个 [AfterCommit](#after-commit)。

> [!WARNING]
> 在回调内部，所有操作都必须使用传入的 `tx`，而不是外层的 `db`。使用 `db` 的操作不属于这个事务，回滚时也不会被撤销。

把 `tx` 作为第二个参数再调用 `database.RunInTx`，内层会以保存点（savepoint）执行：内层返回错误时只撤销内层的修改，外层可以继续。

<a id="after-commit"></a>
### 提交后执行

广播事件、删除缓存、调用外部服务这类操作不能随事务回滚，应当在事务提交之后执行：提前广播，浏览器可能收到一条随后被回滚的通知；提前删除缓存，并发的读请求会把还没提交的旧数据重新写进缓存。在事务中用 `tx.AfterCommit` 登记它们。下面在[发布文章](actions.md#writing-actions)的事务中登记一次[广播](broadcasting.md#publishing-events)，`a.hub` 由 action 的构造函数传入：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // ……加锁读取、检查权限并更新文章
    tx.AfterCommit(func(ctx context.Context) error {
        return a.hub.PublishJSON(ctx, "articles", ArticlePublished{ID: item.ID, Title: item.Title})
    })
    return nil
})
```

- **提交成功后才执行。** 回调在 `RunInTx` 返回之前按登记顺序执行；事务回滚时全部丢弃。
- **保存点中登记的回调同样等最外层提交。** 保存点回滚时只丢弃它自己登记的回调。因此接收 `tx database.Tx` 的辅助函数可以直接登记，不用关心自己是否在最外层。
- **请求结束不会打断回调。** 回调收到的 `ctx` 去掉了取消信号，提交之后客户端断开，回调照常执行。
- **失败只记录日志。** 回调返回错误或 panic 时记录一条 Error 日志，其余回调继续执行，`RunInTx` 仍然返回 `nil`：数据已经提交，调用方不应以为操作失败。返回错误前用 `errors.Wrap` 写明是哪个操作，日志中才看得出来。

> [!WARNING]
> 回调只保存在内存中，事务提交之后、回调执行之前进程退出，回调就不会执行。它适合尽力而为的操作；必须完成的工作，例如发送通知邮件，请在事务中发布任务，任务与业务数据一起提交，见[队列](queue.md#publishing-jobs)。

回调是同步执行的，会推迟响应，耗时的操作交给队列。

<a id="isolation-and-deadlocks"></a>
### 隔离级别与死锁

PostgreSQL 的默认隔离级别是 READ COMMITTED。“先读取、检查、再修改”的逻辑应当放在一个事务中，并用 `FOR UPDATE` 锁住要修改的行，见[在事务中检查](authorization.md#checking-in-transactions)。

数据库检测到死锁时会中止其中一个事务，`database.IsDeadlock` 可以识别这个错误。被中止的事务已经整体回滚，登记的提交后回调也一并丢弃，可以直接重试：

```go
for attempt := 1; ; attempt++ {
    err := database.RunInTx(ctx, a.db, nil, publish)
    if database.IsDeadlock(err) && attempt < 3 {
        continue
    }
    return err
}
```

<a id="constraint-errors"></a>
## 处理约束错误

数据库约束是保证数据一致性的最后一道防线。当插入的数据违反唯一约束时，例如两篇文章使用了相同的 slug，你通常希望返回一个 `409 Conflict`，而不是 `500`。下面是“创建文章”的 `Create` action 的 `Handle`，`articles_slug_key` 是[迁移](migrations.md#migration-structure)中定义的唯一约束：

```go
func (a *Create) Handle(ctx context.Context, subject identity.Subject, c Changes) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item := &Article{
        ID: uuid.Must(uuid.NewV7()), AuthorID: subject.ID, Status: StatusDraft,
        Title: c.Title, Slug: c.Slug, Content: c.Content,
    }
    _, err := a.db.NewInsert().Model(item).Exec(ctx)
    if database.IsUniqueViolation(err, "articles_slug_key") {
        return nil, ErrSlugTaken.Wrap(err, errorx.Public("slug", c.Slug))
    }
    if err != nil {
        return nil, errors.Wrap(err, "insert article")
    }
    return item, nil
}
```

`database` 包提供以下判断函数：

| 函数 | 判断的约束 | SQLSTATE |
| --- | --- | --- |
| `IsUniqueViolation(err, names...)` | 唯一约束与主键 | 23505 |
| `IsForeignKeyViolation(err)` | 外键约束 | 23503 |
| `IsCheckViolation(err)` | 检查约束 | 23514 |
| `IsDeadlock(err)` | 死锁 | 40P01 |

`IsUniqueViolation` 可以传入约束名，只在指定的约束冲突时返回 `true`。推荐总是指定约束名，为不同的约束返回不同的业务错误，例如“slug 已被使用”和“邮箱已注册”；其他错误则保留原因继续返回。

<a id="testing"></a>
## 测试

`database/dbtest` 包可以为每个测试创建一个独立的真实数据库，执行迁移后交给测试使用，测试结束时自动删除。详见[测试](testing.md#database-tests)。
