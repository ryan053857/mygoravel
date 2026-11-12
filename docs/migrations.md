# 数据库：迁移

- [简介](#introduction)
- [编写迁移](#writing-migrations)
    - [迁移文件结构](#migration-structure)
- [嵌入迁移文件](#embedding-migrations)
- [发布框架迁移](#publishing-framework-migrations)
- [运行迁移](#running-migrations)
    - [迁移命令](#migration-commands)
    - [在代码中执行迁移](#creating-a-provider)
- [填充数据](#seeding)
- [修改已发布的迁移](#published-migrations)
    - [在 CI 中检查](#checking-in-ci)
    - [零停机变更](#zero-downtime-changes)
- [非事务迁移失败后的恢复](#recovering-non-transactional-migrations)

<a id="introduction"></a>
## 简介

迁移就像是数据库的版本控制：它记录了表结构的每一次变化，让团队成员和每个部署环境都能把数据库升级到相同的结构。

mygoravel 使用 [goose](https://github.com/pressly/goose) 管理迁移。迁移是应用目录中的普通 SQL 文件，它们会被嵌入到二进制中，在发布时通过命令显式执行——API 服务启动时**不会**自动执行迁移。

<a id="writing-migrations"></a>
## 编写迁移

用 `migrate make` 新建迁移。名称使用 snake_case，以动作开头、表名结尾：

```bash
go run ./cmd/api migrate make create_articles_table
```

命令输出新文件的路径，例如 `migrations/20261111090000_create_articles_table.sql`。文件名是 UTC 时间戳加名称，版本保证大于目录中已有的迁移，所以新迁移总是排在最后。命令按 `migratecmd.New` 传入的驱动生成对应数据库的骨架，不加载配置、不连接数据库；`--dir` 默认是 `migrations`。

与 Laravel 一样，命令会从名称推断表名并生成骨架：

| 名称 | 生成的骨架 |
| --- | --- |
| `create_articles_table` | `CREATE TABLE articles`，带 UUID 主键 `id`、`created_at`、`updated_at`；`Down` 中 `DROP TABLE articles`。SQLite 中这三列都是 `TEXT`，MySQL 中是 `CHAR(36)` 和 `DATETIME(6)` |
| `add_published_at_to_articles_table`、`drop_summary_from_articles_table` | `ALTER TABLE articles`，补全后才能执行 |
| 其他名称，例如 `backfill_article_slugs` | 只有 goose 注解 |

时间戳使用 goose 的格式 `20261111090000`，而不是 Laravel 的 `2026_11_11_090000`：goose 把第一个下划线之前的数字当作版本号。

应用还没有任何迁移时，`migrations/embed.go` 中的 `//go:embed *.sql` 无法编译，应用的 `migrate make` 也就无法运行。这时用一个不导入迁移包的独立入口创建第一个迁移，见[发布框架迁移](#publishing-framework-migrations)末尾的 `cmd/publish/main.go`。

<a id="migration-structure"></a>
### 迁移文件结构

每个迁移文件包含两部分：`Up` 描述如何应用这次变更，`Down` 描述如何撤销它。下面是补全后的 `create_articles_table`：

```sql
-- +goose Up
CREATE TABLE articles (
    id           uuid PRIMARY KEY,
    author_id    text NOT NULL,
    title        text NOT NULL,
    slug         text NOT NULL,
    content      text NOT NULL,
    status       text NOT NULL DEFAULT 'draft',
    published_at timestamptz,
    created_at   timestamptz NOT NULL,
    updated_at   timestamptz NOT NULL,
    deleted_at   timestamptz,
    CONSTRAINT articles_slug_key UNIQUE (slug)
);

-- +goose Down
DROP TABLE articles;
```

这里显式声明了三个时间字段，与[文章模型](database.md#defining-models)保持一致。时间列不设数据库默认值，`created_at` 和 `updated_at` 由[模型钩子](database.md#timestamps)写入，三种数据库的规则相同；允许为空的 `deleted_at` 配合 Bun 的[软删除标签](database.md#soft-deletes)使用。模型标签不会自动修改数据库表结构。

默认情况下，每个迁移文件在一个事务中执行，失败时整体回滚。

<a id="embedding-migrations"></a>
## 嵌入迁移文件

为了让部署时只需要一个二进制文件，请用 `embed` 把迁移文件嵌入进来。在 `migrations` 目录中创建 `embed.go`：

```go
// Package migrations 嵌入应用的 SQL 迁移文件。
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
```

这样 `migrations` 就成了一个普通的 Go 包，命令行和测试都可以通过 `migrations.FS` 使用这些文件。每次新增迁移文件后，都需要重新构建应用。

<a id="publishing-framework-migrations"></a>
## 发布框架迁移

文件上传等可选功能自带迁移 SQL。使用这些功能时，把迁移发布到你的应用目录，审查并纳入版本控制，然后交给应用现有的 goose 迁移流程执行。

发布命令需要显式注册。下面是加入已有命令装配代码的片段：`root` 是 `command.Root` 返回的根命令，`migrations.FS` 是应用嵌入的迁移，`dbConfig` 是应用命令装配中从配置取出数据库配置的函数。需要导入 `command/migratecmd`、`database/migrate`、`database/postgres` 和 `filesystem` 包：

```go
migrationCmd := migratecmd.New(migrations.FS, postgres.Driver, dbConfig)
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "filesystem", FS: filesystem.Migrations()},
))
root.AddCommand(migrationCmd)
```

`Publish` 的第一个参数是应用使用的数据库驱动。框架迁移按数据库类型各写了一份，发布时只复制对应的那一份；使用 SQLite 或 MySQL 时传入 `sqlite.Driver` 或 `mysql.Driver`。

在应用根目录执行：

```bash
go run ./cmd/api migrate publish --dir migrations
```

`--dir` 默认是当前工作目录下的 `migrations`。命令只输出本次新生成的文件名；再次运行且没有新增迁移时，没有输出。发布过程不加载应用配置，不连接数据库或存储，也不执行 SQL。

每个发布文件的第一行包含稳定的来源标记，例如：

```sql
-- mygoravel: filesystem/0001_create_files
```

发布器通过这个标记识别已有迁移。你可以在首次执行前调整 SQL 或重命名文件，但应保留标记；再次发布不会覆盖你的修改。升级框架后，再次运行命令只会发布新增的来源迁移。

新文件的版本是当前 UTC 时间戳；目录中已有更大的版本时，从最大版本加一开始，因此新迁移总是排在已有迁移之后。目标文件已存在时发布器报错而不会覆盖。发布中途遇到 I/O 错误时，已完成的文件会保留，修复后重试即可。

> [!NOTE]
> 发布完成后，需要审查和提交 SQL，再重新构建应用，将新文件嵌入二进制。然后运行 `migrate up`。已经在任何环境执行过的迁移不能修改，后续调整必须新增迁移。`CheckPublished` 继续用于检查应用自身的历史基线，不用于比较框架源 SQL 与应用定制后的 SQL。

如果应用还没有任何 SQL，`//go:embed *.sql` 暂时无法编译。可以先创建独立的入口，不导入应用的迁移包。下面是完整的 `cmd/publish/main.go`，它同时提供 `make` 和 `publish`：

```go
package main

import (
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/command/migratecmd"
    "github.com/ryan053857/mygoravel/database/migrate"
    "github.com/ryan053857/mygoravel/database/postgres"
    "github.com/ryan053857/mygoravel/filesystem"
)

func main() {
    root := command.Root("migrations", "新建或发布迁移")
    root.AddCommand(
        migratecmd.Make(postgres.Driver),
        migratecmd.Publish(postgres.Driver, migrate.Source{Name: "filesystem", FS: filesystem.Migrations()}),
    )
    command.Execute(root)
}
```

`migratecmd.Make` 和 `migratecmd.Publish` 的第一个参数都是应用使用的数据库驱动。执行 `go run ./cmd/publish make create_articles_table` 或 `go run ./cmd/publish publish --dir migrations` 生成第一个 SQL 后，再添加应用的 `migrations/embed.go` 和迁移执行命令。这个入口也可以一直保留，供开发时使用。

自定义命令或工具也可以直接调用 `migrate.Publish("migrations", postgres.Driver, sources...)`；`sources` 是应用显式选择的 `[]migrate.Source`。每个来源的 `Name` 是稳定的包标识，`FS` 根目录下按数据库类型分为 `postgres`、`sqlite` 等目录，每个目录直接包含带数字版本的 SQL。来源按传入顺序发布，同一来源的文件按数字版本排序。各数据库的同名文件描述同一次变更，共用一个来源标记。

<a id="running-migrations"></a>
## 运行迁移

<a id="migration-commands"></a>
### 迁移命令

在[装配命令行](commands.md#declaring-the-cli)时加入 `command/migratecmd` 包的 `migratecmd.New`，应用就有了 `migrate up`、`migrate status`、`migrate down` 和[新建迁移](#writing-migrations)的 `migrate make`：

```go
root.AddCommand(migratecmd.New(migrations.FS, postgres.Driver, func(cfg settings.Config) database.Config {
    return cfg.Database
}))
```

之后，你就可以在部署流程中执行：

```bash
./bin/api migrate up --config config/app.yaml --config config/prod.yaml
./bin/api migrate status --config config/app.yaml --config config/prod.yaml
```

命令负责加载配置、连接数据库和关闭连接。数据库连不上时，命令以 `ExitUnavailable` 退出，部署脚本可以据此重试。各命令的输出见 [migrate 命令](commands.md#migrate-command)。

迁移成功后，再启动依赖新表结构的 API 服务。完整的发布流程见[部署](deployment.md#running-migrations)。

<a id="creating-a-provider"></a>
### 在代码中执行迁移

在测试或自定义命令中，可以用 `migrate.NewProvider` 创建一个 goose 迁移器（Provider），然后调用它的方法：

```go
provider, err := migrate.NewProvider(db, migrations.FS)
if err != nil {
    return err
}
err = migrate.Up(ctx, provider)
```

其中 `db` 是 `database.Open` 打开的 `*bun.DB`，迁移器按它的方言执行。`NewProvider` 要求文件系统的**根目录**直接包含迁移文件，`migrations.FS` 正好满足这个要求。如果你的迁移文件嵌入在某个子目录中，可以先用 `migrate.Sub(files, "migrations")` 取出子目录。

`NewProvider` 返回的是原生的 `*goose.Provider`，你可以直接调用它的 `Up`、`Down`、`Status` 等方法。它已经配置好了以下行为：

- 在 PostgreSQL 和 MySQL 上使用会话锁（MySQL 为 `GET_LOCK`），保证多个实例同时执行迁移时也只会串行运行；SQLite 只有一个数据库文件，不加锁；
- 只使用 SQL 文件，不使用 goose 的全局 Go 迁移注册。

版本记录表、事务和锁都由 goose 管理。`migrate.Up(ctx, provider)` 在调用 `provider.Up` 的基础上为错误附加了上下文信息。`migrate` 命令使用的也是同一个迁移器。

<a id="seeding"></a>
## 填充数据

数据库中的初始数据分为两类，放在不同的地方：

- **每个环境都必须有的数据**，例如角色或分类，写进迁移的 `Up` 部分。它们和表结构一起受版本控制，在每个环境中只执行一次。
- **只在开发和演示时需要的数据**，例如几篇示例文章，写成一个应用自己的 `seed` 命令。

下面是博客应用的 `internal/bootstrap/seed.go`，它以 alice 的身份创建并发布两篇文章。`db` 由 [bootstrap.Commands](actions.md#as-commands) 在命令执行时装配：

```go
package bootstrap

import (
    "errors"

    "github.com/spf13/cobra"
    "github.com/uptrace/bun"

    "example.com/blog/internal/article"
    "example.com/blog/internal/user"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/identity"
)

func seed(db *bun.DB) *cobra.Command {
    return &cobra.Command{
        Use:   "seed",
        Short: "写入开发用的示例数据",
        Args:  command.NoArgs,
        RunE: func(cmd *cobra.Command, _ []string) error {
            create, publish := article.NewCreate(db), article.NewPublish(db)
            alice := identity.Subject{Type: user.Type, ID: "alice"}
            for _, c := range []article.Changes{
                {Title: "Hello World", Slug: "hello-world", Content: "……"},
                {Title: "Second Post", Slug: "second-post", Content: "……"},
            } {
                item, err := create.Handle(cmd.Context(), alice, c)
                if errors.Is(err, article.ErrSlugTaken) {
                    continue // 之前已经写入过
                }
                if err != nil {
                    return err
                }
                if _, err := publish.Handle(cmd.Context(), alice, item.ID); err != nil {
                    return err
                }
            }
            return nil
        },
    }
}
```

示例数据通过 [action](actions.md) 写入，而不是直接插入表中，这样它们和真实请求创建的数据经过相同的验证与规则，表结构变化时也不需要单独维护。slug 的唯一约束让命令可以重复执行：已经存在的文章会被跳过。

`seed` 不是某一个 action 的入口，它组合了 `Create` 和 `Publish` 两个动作，直接调用它们的 `Handle`。在本地准备数据库：

```bash
go run ./cmd/api migrate up
go run ./cmd/api seed
```

`seed` 只用于开发环境，不要把它加入部署流程。需要大量随机数据时，可以用 [gofakeit](https://github.com/brianvoe/gofakeit) 生成 `article.Changes`。测试不依赖这些数据，每个测试在自己的数据库中准备数据，见[测试 action](testing.md#testing-actions)。

<a id="published-migrations"></a>
## 修改已发布的迁移

一旦迁移被发布到任何环境，就**不要再修改它**。已经执行过的迁移不会再次执行，修改它只会让不同环境的数据库结构变得不一致。如果需要修正，请新建一个迁移。

<a id="checking-in-ci"></a>
### 在 CI 中检查

为了防止有人不小心修改了已发布的迁移，你可以在 CI 中把当前的迁移目录与某个已发布版本（例如上一个发布标签）进行比较：

```go
err := migrate.CheckPublished("migrations", "baseline/migrations")
```

如果你的迁移已经以 `fs.FS` 的形式存在，可以使用 `migrate.CheckFS(currentFS, baselineFS)`。

检查会确认基线中的每个迁移文件在当前目录中都存在，并且内容逐字节相同；当前目录中新增的文件是允许的。

<a id="zero-downtime-changes"></a>
### 零停机变更

滚动发布时，新旧两个版本的应用会同时运行一段时间，它们必须都能使用当前的数据库结构。因此，破坏性的变更（例如重命名列）应该分多次发布完成：

1. 先添加新结构（例如新增一列），新旧代码都能正常工作；
2. 部署使用新结构的应用代码；
3. 在后续的版本中，移除不再使用的旧结构。

<a id="recovering-non-transactional-migrations"></a>
## 非事务迁移失败后的恢复

某些操作不能在事务中执行，例如 `CREATE INDEX CONCURRENTLY`。这时需要在迁移文件中使用 goose 的 `-- +goose NO TRANSACTION` 指令。MySQL 的 DDL 会隐式提交事务，所以在 MySQL 上**每个**包含 DDL 的迁移都相当于非事务迁移。

> [!WARNING]
> 非事务迁移失败时，可能已有部分语句生效，而迁移的版本号却没有更新。此时直接重新执行可能会失败或造成重复的变更。

遇到这种情况，请按以下步骤恢复：

1. 查询数据库的实际状态，确认哪些语句已经执行；
2. 根据实际结构，手动清理或补全变更；
3. 确认数据库处于迁移前或迁移后的一致状态后，再重新执行迁移。

为了降低风险，建议每个非事务迁移只包含一条语句；使用 MySQL 时，建议每个迁移只包含一条 DDL。
