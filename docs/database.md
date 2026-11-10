# 数据库：快速入门

- [简介](#introduction)
- [配置](#configuration)
    - [使用 SQLite](#sqlite)
    - [使用 MySQL](#mysql)
    - [数据库差异](#dialect-differences)
- [连接数据库](#connecting)
- [定义模型](#defining-models)
    - [主键](#primary-keys)
    - [自动维护时间戳](#timestamps)
    - [软删除](#soft-deletes)
- [执行查询](#running-queries)
    - [处理“未找到”](#handling-not-found)
- [事务](#transactions)
- [处理约束错误](#constraint-errors)
- [测试](#testing)

<a id="introduction"></a>
## 简介

几乎每个 API 都需要与数据库交互。mygoravel 支持 PostgreSQL、SQLite 和 MySQL，并使用以下组件：

- [pgx](https://github.com/jackc/pgx)：PostgreSQL 驱动，由 `database/postgres` 包提供；
- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite)：纯 Go 的 SQLite 驱动，不需要 cgo，由 `database/sqlite` 包提供；
- [go-sql-driver/mysql](https://github.com/go-sql-driver/mysql)：MySQL 驱动，由 `database/mysql` 包提供；
- [Bun](https://bun.uptrace.dev)：SQL 优先的 Go ORM，负责查询构建和模型映射；
- [goose](https://github.com/pressly/goose)：数据库迁移，见[迁移](migrations.md)。

`database` 包负责打开一个配置好的连接池，返回原生的 `*bun.DB`。应用的迁移是按某一种数据库写的，因此数据库类型在代码中通过驱动包选定，而不是在配置中；应用只会编译自己 import 的驱动。之后的查询、事务等操作都直接使用 Bun 的 API，你可以随时参考 [Bun 官方文档](https://bun.uptrace.dev/guide/)。

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
| `dsn` | PostgreSQL 连接串，例如 `postgres://user:pass@localhost:5432/app?sslmode=disable`；SQLite 是数据库文件路径 | 无，必填 |
| `max_open_conns` | 最大连接数；`0` 表示不限制 | 4 × `GOMAXPROCS` |
| `max_idle_conns` | 最大空闲连接数；`0` 表示不保留空闲连接 | 4 × `GOMAXPROCS` |
| `conn_max_lifetime` | 连接的最长存活时间 | `1h` |
| `conn_max_idle_time` | 空闲连接的最长保留时间 | `30m` |

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

`database.Config` 实现了 `Validate` 方法，`config.Load` 会自动检查 DSN 是否为空、连接池参数是否合法；DSN 的格式由驱动在打开连接时检查。

> [!TIP]
> 设置 `max_open_conns` 时，请考虑应用的部署副本数：所有副本的连接数之和不应超过数据库的最大连接数。

<a id="sqlite"></a>
### 使用 SQLite

SQLite 把整个数据库存放在一个文件中，不需要单独的数据库服务，适合只部署一个实例的应用。使用 `database/sqlite` 包的驱动，`dsn` 是文件路径，所在目录必须已经存在：

```yaml
database:
  dsn: storage/app.db
```

```go
db, err := database.Open(ctx, sqlite.Driver, cfg.Database)
```

驱动为每个连接设置以下参数，`dsn` 中不能再带连接参数：

- `journal_mode=WAL`：读和写互不阻塞；
- `busy_timeout=5000`：写入冲突时最多等待 5 秒，而不是立即失败；
- `foreign_keys=ON`：SQLite 默认不检查外键；
- `synchronous=NORMAL`：WAL 模式下安全，写入更快；
- 写事务在 `BEGIN` 时就取得写锁，多个先读后写的事务会依次执行，不会在中途失败。

使用 SQLite 时需要注意：

- SQLite 没有时间类型，Bun 会把时间转换为 UTC 文本写入，按文本比较即可得到正确的时间顺序。因此请让所有时间都经过 Bun 写入，不要在迁移中使用 `DEFAULT CURRENT_TIMESTAMP`，它写入的文本格式不同；
- 写事务已经独占了数据库，不需要也不支持 `SELECT ... FOR UPDATE`，在 SQLite 上请去掉 `For("UPDATE")`，见[在事务中检查](authorization.md#checking-in-transactions)；
- 每个事务在 `BEGIN` 时都会取得写锁，只读的事务请传入 `&sql.TxOptions{ReadOnly: true}`，它不取写锁，也不会让写入排队；
- `ALTER TABLE` 只支持部分操作，修改列需要重建表，见 [SQLite 文档](https://www.sqlite.org/lang_altertable.html#otheralter)。

部署和备份见[使用 SQLite 部署](deployment.md#sqlite)。

<a id="mysql"></a>
### 使用 MySQL

mygoravel 支持 MySQL 8.4 LTS，不保证兼容 MariaDB。使用 `database/mysql` 包的驱动，`dsn` 是 URL：

```yaml
database:
  dsn: mysql://blog:${DATABASE_PASSWORD}@localhost:3306/blog
```

```go
db, err := database.Open(ctx, mysql.Driver, cfg.Database)
```

URL 上的查询参数原样交给 go-sql-driver/mysql，例如 `?tls=true`。以下设置由驱动固定，`dsn` 中的同名参数会被覆盖：

- 时间按 UTC 读写：连接的 `time_zone` 是 `+00:00`，写入的时间先转换为 UTC，读出的 `DATETIME` 是 UTC 的 `time.Time`；
- `RowsAffected` 返回 `WHERE` 匹配到的行数，与 PostgreSQL 和 SQLite 一致。MySQL 默认返回实际改变的行数，值没有变化的 `UPDATE` 会返回 0。

使用 MySQL 时需要注意：

- 时间列请声明为 `DATETIME(6)`，`DATETIME` 只精确到秒；
- 默认的排序规则比较字符串时不区分大小写，`'Hello' = 'hello'` 为真，唯一约束也是如此。需要区分大小写的列（例如 token）请声明 `COLLATE utf8mb4_bin`；
- DDL 会隐式提交事务，迁移失败时已经执行的语句不会回滚，见[迁移失败后的恢复](migrations.md#recovering-non-transactional-migrations)；
- 不支持 `RETURNING`。

<a id="dialect-differences"></a>
### 数据库差异

数据库类型由驱动包选定，Bun 的查询在三种数据库上写法相同，框架的会话、锁、限流和文件服务在三种数据库上行为一致（[队列](queue.md)目前只支持 PostgreSQL）。仍然需要留意的是数据库本身的差异：

| | PostgreSQL | SQLite | MySQL |
| --- | --- | --- | --- |
| 驱动与 `dsn` | `postgres.Driver`，`postgres://` 连接串 | `sqlite.Driver`，文件路径 | `mysql.Driver`，`mysql://` URL |
| 约束错误按什么匹配 | 约束名 | `表.列` | 约束名或索引名 |
| `SELECT ... FOR UPDATE` | 支持 | 不需要，写事务独占数据库 | 支持 |
| DDL 能否回滚 | 能 | 能 | 不能 |
| 字符串比较 | 区分大小写 | 区分大小写 | 默认不区分 |
| `RETURNING` | 支持 | 支持 | 不支持 |
| 部署 | 多实例 | 单实例 | 多实例 |


<a id="connecting"></a>
## 连接数据库

使用 `database.Open` 打开连接池，第二个参数是应用使用的数据库驱动。它会立即 Ping 数据库确认连通，失败时自动释放已打开的资源。下面是一个完整的连接检查函数：

```go
package storage

import (
    "context"

    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/database/postgres"
)

func Check(ctx context.Context, dsn string) error {
    cfg := database.DefaultConfig()
    cfg.DSN = dsn
    db, err := database.Open(ctx, postgres.Driver, cfg)
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

mygoravel 约定主键使用 UUIDv7，由应用在插入前生成，迁移中声明为 `id uuid PRIMARY KEY`（SQLite 中为 `id TEXT PRIMARY KEY`，MySQL 中为 `id CHAR(36) PRIMARY KEY`）：

```go
item := &Article{ID: uuid.Must(uuid.NewV7()), AuthorID: subject.ID}
```

在插入之前就有 ID，同一事务中的关联记录、对象存储的键或者幂等重试都可以直接使用它。UUIDv7 以毫秒时间戳开头，新记录总是追加在索引末尾，写入性能接近自增 ID；对外暴露时也不会泄露记录数量或被逐个遍历，但它包含创建时间，也不能代替[授权](authorization.md)检查。只在内部使用、不出现在接口中的大表，可以在迁移中改用 `bigint GENERATED ALWAYS AS IDENTITY`。

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

如果使用 `Set(...)` 显式指定更新表达式，需要同时设置 `Set("updated_at = ?", time.Now().UTC())`。`Model((*Article)(nil))` 没有模型实例可供此钩子修改，原生 SQL 也不会调用模型钩子；这些写入路径需要自行维护时间戳。迁移中的时间列不设默认值，时间总是由应用写入。

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

软删除的记录仍然参与数据库唯一约束。本例的 `articles_slug_key` 会继续保留已删除文章的 slug；如果业务允许复用，可在迁移中改为仅覆盖 `deleted_at IS NULL` 的唯一索引，见 Bun 的[唯一索引说明](https://bun.uptrace.dev/guide/soft-deletes.html#unique-indexes)。

<a id="running-queries"></a>
## 执行查询

在业务服务中，使用注入的 `db` 构建查询：

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

数据库约束是保证数据一致性的最后一道防线。当插入的数据违反唯一约束时，例如两篇文章使用了相同的 slug，你通常希望返回一个 `409 Conflict`，而不是 `500`。下面是创建文章的完整实现，`articles_slug_key` 是[迁移](migrations.md#migration-structure)中定义的唯一约束：

```go
func (s *Service) Create(ctx context.Context, subject identity.Subject, c Changes) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item := &Article{
        ID: uuid.Must(uuid.NewV7()), AuthorID: subject.ID, Status: StatusDraft,
        Title: c.Title, Slug: c.Slug, Content: c.Content,
    }
    _, err := s.db.NewInsert().Model(item).Exec(ctx)
    if postgres.IsUniqueViolation(err, "articles_slug_key") {
        return nil, ErrSlugTaken.Wrap(err, errorx.Public("slug", c.Slug))
    }
    if err != nil {
        return nil, errors.Wrap(err, "insert article")
    }
    return item, nil
}
```

判断函数由驱动包提供，`postgres`、`sqlite` 和 `mysql` 包中各有一组同名函数：

| 函数 | 判断的约束 | PostgreSQL | SQLite | MySQL |
| --- | --- | --- | --- | --- |
| `IsUniqueViolation(err, names...)` | 唯一约束与主键 | 23505 | `SQLITE_CONSTRAINT_UNIQUE`、`SQLITE_CONSTRAINT_PRIMARYKEY` | 1062 |
| `IsForeignKeyViolation(err)` | 外键约束 | 23503 | `SQLITE_CONSTRAINT_FOREIGNKEY` | 1451、1452 |
| `IsCheckViolation(err)` | 检查约束 | 23514 | `SQLITE_CONSTRAINT_CHECK` | 3819 |

`IsUniqueViolation` 可以传入约束名，只在指定的约束冲突时返回 `true`。推荐总是指定约束名，为不同的约束返回不同的业务错误，例如“slug 已被使用”和“邮箱已注册”；其他错误则保留原因继续返回。

SQLite 的错误中没有约束名，只有冲突的列，因此在 SQLite 上按 `表.列` 匹配，多列之间用 `, ` 分隔；表达式索引则按索引名匹配：

```go
sqlite.IsUniqueViolation(err, "articles.slug")
sqlite.IsUniqueViolation(err, "files.disk, files.key")
```

MySQL 与 PostgreSQL 一样按约束名或索引名匹配，主键的名字是 `PRIMARY`。

<a id="testing"></a>
## 测试

`database/dbtest` 包可以为每个测试创建一个独立的真实数据库，执行迁移后交给测试使用，测试结束时自动删除。详见[测试](testing.md#database-tests)。
