# 数据库：迁移

- [简介](#introduction)
- [编写迁移](#writing-migrations)
    - [迁移文件结构](#migration-structure)
- [嵌入迁移文件](#embedding-migrations)
- [发布框架迁移](#publishing-framework-migrations)
- [运行迁移](#running-migrations)
    - [迁移命令](#migration-commands)
    - [在代码中执行迁移](#creating-a-provider)
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

在应用的 `migrations` 目录中新建一个 SQL 文件。文件名以一个唯一、递增的 UTC 时间戳开头，后面跟上描述，例如 `20261111090000_create_articles.sql`。

你也可以使用 goose 的命令行工具生成文件：

```bash
goose -dir migrations create create_articles sql
```

<a id="migration-structure"></a>
### 迁移文件结构

每个迁移文件包含两部分：`Up` 描述如何应用这次变更，`Down` 描述如何撤销它：

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
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    deleted_at   timestamptz,
    CONSTRAINT articles_slug_key UNIQUE (slug)
);

-- +goose Down
DROP TABLE articles;
```

这里显式声明了三个时间字段，与[文章模型](database.md#defining-models)保持一致。`DEFAULT now()` 仅在插入未提供列值或使用 `DEFAULT` 时生效；`updated_at` 的更新由[模型钩子](database.md#timestamps)维护，允许为空的 `deleted_at` 配合 Bun 的[软删除标签](database.md#soft-deletes)使用。模型标签不会自动修改数据库表结构。

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

发布命令需要显式注册。下面是加入已有命令装配代码的片段：`root` 是 `command.Root` 返回的根命令，`migrations.FS` 是应用嵌入的迁移，`dbConfig` 是应用命令装配中从配置取出数据库配置的函数。需要导入 `command/migratecmd`、`database/migrate` 和 `files` 包：

```go
migrationCmd := migratecmd.New(migrations.FS, dbConfig)
migrationCmd.AddCommand(migratecmd.Publish(
    migrate.Source{Name: "files", FS: files.Migrations()},
))
root.AddCommand(migrationCmd)
```

在应用根目录执行：

```bash
go run ./cmd/api migrate publish --dir migrations
```

`--dir` 默认是当前工作目录下的 `migrations`。命令只输出本次新生成的文件名；再次运行且没有新增迁移时，没有输出。发布过程不加载应用配置，不连接数据库或存储，也不执行 SQL。

每个发布文件的第一行包含稳定的来源标记，例如：

```sql
-- mygoravel: files/0001_create_files
```

发布器通过这个标记识别已有迁移。你可以在首次执行前调整 SQL 或重命名文件，但应保留标记；再次发布不会覆盖你的修改。升级框架后，再次运行命令只会发布新增的来源迁移。

新文件的版本是当前 UTC 时间戳；目录中已有更大的版本时，从最大版本加一开始，因此新迁移总是排在已有迁移之后。目标文件已存在时发布器报错而不会覆盖。发布中途遇到 I/O 错误时，已完成的文件会保留，修复后重试即可。

> [!NOTE]
> 发布完成后，需要审查和提交 SQL，再重新构建应用，将新文件嵌入二进制。然后运行 `migrate up`。已经在任何环境执行过的迁移不能修改，后续调整必须新增迁移。`CheckPublished` 继续用于检查应用自身的历史基线，不用于比较框架源 SQL 与应用定制后的 SQL。

如果应用还没有任何 SQL，`//go:embed *.sql` 暂时无法编译。可以先创建独立的发布入口，不导入应用的迁移包。下面是完整的 `cmd/publish/main.go`：

```go
package main

import (
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/command/migratecmd"
    "github.com/ryan053857/mygoravel/database/migrate"
    "github.com/ryan053857/mygoravel/files"
)

func main() {
    root := command.Root("publish-migrations", "发布应用需要的迁移")
    root.AddCommand(migratecmd.Publish(
        migrate.Source{Name: "files", FS: files.Migrations()},
    ))
    command.Execute(root)
}
```

执行 `go run ./cmd/publish publish --dir migrations` 后，再添加应用的 `migrations/embed.go` 和迁移执行命令。这个发布入口也可以一直保留，供开发时使用。

自定义命令或工具也可以直接调用 `migrate.Publish("migrations", sources...)`；`sources` 是应用显式选择的 `[]migrate.Source`。每个来源的 `Name` 是稳定的包标识，`FS` 根目录直接包含带数字版本的 SQL。来源按传入顺序发布，同一来源的文件按数字版本排序。

<a id="running-migrations"></a>
## 运行迁移

<a id="migration-commands"></a>
### 迁移命令

在[装配命令行](commands.md#declaring-the-cli)时加入 `command/migratecmd` 包的 `migratecmd.New`，应用就有了 `migrate up`、`migrate status` 和 `migrate down` 三个命令：

```go
root.AddCommand(migratecmd.New(migrations.FS, func(cfg settings.Config) database.Config {
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
provider, err := migrate.NewProvider(db.DB, migrations.FS)
if err != nil {
    return err
}
err = migrate.Up(ctx, provider)
```

其中 `db` 是 `database.Open` 打开的 `*bun.DB`，`db.DB` 是它底层的 `*sql.DB`。`NewProvider` 要求文件系统的**根目录**直接包含迁移文件，`migrations.FS` 正好满足这个要求。如果你的迁移文件嵌入在某个子目录中，可以先用 `migrate.Sub(files, "migrations")` 取出子目录。

`NewProvider` 返回的是原生的 `*goose.Provider`，你可以直接调用它的 `Up`、`Down`、`Status` 等方法。它已经配置好了以下行为：

- 使用 PostgreSQL 的会话锁，保证多个实例同时执行迁移时也只会串行运行；
- 只使用 SQL 文件，不使用 goose 的全局 Go 迁移注册。

版本记录表、事务和锁都由 goose 管理。`migrate.Up(ctx, provider)` 在调用 `provider.Up` 的基础上为错误附加了上下文信息。`migrate` 命令使用的也是同一个迁移器。

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

某些操作不能在事务中执行，例如 `CREATE INDEX CONCURRENTLY`。这时需要在迁移文件中使用 goose 的 `-- +goose NO TRANSACTION` 指令。

> [!WARNING]
> 非事务迁移失败时，可能已有部分语句生效，而迁移的版本号却没有更新。此时直接重新执行可能会失败或造成重复的变更。

遇到这种情况，请按以下步骤恢复：

1. 查询数据库的实际状态，确认哪些语句已经执行；
2. 根据实际结构，手动清理或补全变更；
3. 确认数据库处于迁移前或迁移后的一致状态后，再重新执行迁移。

为了降低风险，建议每个非事务迁移只包含一条语句。
