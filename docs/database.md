# 数据库：快速入门

- [简介](#introduction)
- [配置](#configuration)
- [连接数据库](#connecting)
- [定义模型](#defining-models)
- [执行查询](#running-queries)
    - [处理“未找到”](#handling-not-found)
- [事务](#transactions)
- [处理约束错误](#constraint-errors)
- [测试](#testing)

<a id="introduction"></a>
## 简介

几乎每个 API 都需要与数据库交互。mygoravel 目前支持 PostgreSQL，并使用以下组件：

- [pgx](https://github.com/jackc/pgx)：PostgreSQL 驱动；
- [Bun](https://bun.uptrace.dev)：SQL 优先的 Go ORM，负责查询构建和模型映射；
- [goose](https://github.com/pressly/goose)：数据库迁移，见[迁移](migrations.md)。

`database` 包负责打开一个配置好的连接池，返回原生的 `*bun.DB`。之后的查询、事务等操作都直接使用 Bun 的 API，你可以随时参考 [Bun 官方文档](https://bun.uptrace.dev/guide/)。

<a id="configuration"></a>
## 配置

数据库配置通常放在配置文件的 `database` 段中：

```yaml
database:
  dsn: ${DATABASE_URL}
  max_open_conns: 20
  max_idle_conns: 10
  conn_max_lifetime: 1h
  conn_max_idle_time: 30m
```

| 字段 | 说明 | `DefaultConfig` 中的默认值 |
| --- | --- | --- |
| `dsn` | PostgreSQL 连接串，例如 `postgres://user:pass@localhost:5432/app?sslmode=disable` | 无，必填 |
| `max_open_conns` | 最大连接数；`0` 表示不限制 | 4 × `GOMAXPROCS` |
| `max_idle_conns` | 最大空闲连接数；`0` 表示不保留空闲连接 | 4 × `GOMAXPROCS` |
| `conn_max_lifetime` | 连接的最长存活时间 | `1h` |
| `conn_max_idle_time` | 空闲连接的最长保留时间 | `30m` |

这些字段对应 `database.Config` 结构体。把它放进应用的根配置结构体，并用 `database.DefaultConfig()` 作为默认值：

```go
type Config struct {
    Database database.Config `yaml:"database"`
}

cfg := Config{Database: database.DefaultConfig()}
if err := config.Load(path, &cfg); err != nil {
    return err
}
```

`database.Config` 实现了 `Validate` 方法，`config.Load` 会自动检查 DSN 是否为空、连接池参数是否合法。

> [!TIP]
> 设置 `max_open_conns` 时，请考虑应用的部署副本数：所有副本的连接数之和不应超过数据库的最大连接数。

<a id="connecting"></a>
## 连接数据库

使用 `database.Open` 打开连接池。它会立即 Ping 数据库确认连通，失败时自动释放已打开的资源。下面是一个完整的连接检查函数：

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

在应用中，连接池应当在入口打开**一次**，然后传给所有需要它的业务服务，在应用退出时关闭，见[依赖装配](dependencies.md)。

<a id="defining-models"></a>
## 定义模型

模型是一个带有 `bun` 标签的普通 Go 结构体，描述数据表的列与字段之间的映射：

```go
type Note struct {
    ID        uuid.UUID `bun:"id,pk"`
    OwnerID   string    `bun:"owner_id,notnull"`
    Title     string    `bun:"title,notnull"`
    CreatedAt time.Time `bun:"created_at,notnull"`
}
```

模型只用于查询映射，表结构本身由[迁移](migrations.md)管理。

<a id="running-queries"></a>
## 执行查询

在业务服务中，使用注入的 `db` 构建查询：

```go
var item Note
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
_, err = db.NewDelete().Model((*Note)(nil)).Where("id = ?", id).Exec(ctx)
```

<a id="handling-not-found"></a>
### 处理“未找到”

查询单条记录时，如果没有匹配的行，Bun 会返回 `sql.ErrNoRows`。请把它转换为业务的“未找到”错误，其他数据库错误则原样返回：

```go
if errors.Is(err, sql.ErrNoRows) {
    return nil, ErrNotFound.New()
}
if err != nil {
    return nil, err
}
```

未经转换的数据库错误会被当作内部错误，返回 `500` 并记录日志。

<a id="transactions"></a>
## 事务

使用 Bun 的 `RunInTx` 在事务中执行一组操作。回调返回错误时事务回滚，返回 `nil` 时提交：

```go
err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
    if _, err := tx.NewInsert().Model(item).Exec(ctx); err != nil {
        return err
    }
    _, err := tx.NewInsert().Model(audit).Exec(ctx)
    return err
})
```

> [!WARNING]
> 在回调内部，所有操作都必须使用传入的 `tx`，而不是外层的 `db`。使用 `db` 的操作不属于这个事务，回滚时也不会被撤销。

请避免在事务中执行网络请求或删除缓存等外部操作。缓存失效应当在事务提交成功之后进行，见[缓存](cache.md#invalidation)。如需嵌套事务或保存点，请参阅 Bun 的相关文档。

<a id="constraint-errors"></a>
## 处理约束错误

数据库约束是保证数据一致性的最后一道防线。当插入的数据违反唯一约束时，你通常希望返回一个 `409 Conflict`，而不是 `500`。`database` 包提供了几个判断函数：

```go
if database.IsUniqueViolation(err, "notes_owner_title_key") {
    return nil, ErrConflict.Wrap(err)
}
```

| 函数 | 判断的约束 | SQLSTATE |
| --- | --- | --- |
| `database.IsUniqueViolation(err, names...)` | 唯一约束 | 23505 |
| `database.IsForeignKeyViolation(err)` | 外键约束 | 23503 |
| `database.IsCheckViolation(err)` | 检查约束 | 23514 |

`IsUniqueViolation` 可以传入约束名，只在指定的约束冲突时返回 `true`。推荐总是指定约束名，为不同的约束返回不同的业务错误，例如“标题重复”和“邮箱已注册”；其他错误则保留原因继续返回。

<a id="testing"></a>
## 测试

`database/dbtest` 包可以为每个测试创建一个独立的真实数据库，执行迁移后交给测试使用，测试结束时自动删除。详见[测试](testing.md#database-tests)。
