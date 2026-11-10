# 命令行

- [简介](#introduction)
- [装配命令行](#declaring-the-cli)
- [内置命令](#built-in-commands)
    - [Run：serve 与常驻进程](#run-command)
    - [openapi 命令](#openapi-command)
    - [routes 命令](#routes-command)
    - [migrate 命令](#migrate-command)
    - [config 命令](#config-command)
- [自定义命令](#custom-commands)
- [输入与输出](#input-and-output)
- [退出码](#exit-codes)

<a id="introduction"></a>
## 简介

一个 API 应用除了启动 HTTP 服务，通常还需要执行一些运维任务：检查配置、执行数据库迁移、导出接口文档、查看路由。mygoravel 的 `command` 包基于 [Cobra](https://github.com/spf13/cobra) 实现了这些命令。每个命令都是一个独立的 `*cobra.Command`，应用按需把它们装配到根命令上，用不到的命令不会出现。

完成本章后，你的应用可以这样使用：

```bash
./bin/api serve
./bin/api migrate up
./bin/api openapi > openapi.json
./bin/api routes --method post
./bin/api serve --config config/app.yaml --config config/prod.yaml
```

<a id="declaring-the-cli"></a>
## 装配命令行

`command.Root` 创建根命令，然后用 Cobra 原生的 `AddCommand` 装配应用需要的命令。下面是博客应用完整的 `internal/cli/cli.go`：

```go
package cli

import (
    "github.com/spf13/cobra"

    "example.com/blog/internal/bootstrap"
    "example.com/blog/internal/settings"
    "example.com/blog/migrations"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/command/migratecmd"
    "github.com/ryan053857/mygoravel/command/restcmd"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/database/postgres"
)

// New 返回应用的根命令。
func New() *cobra.Command {
    root := command.Root("api", "Blog API")
    root.AddCommand(
        command.Run("serve", "启动 HTTP 服务", bootstrap.Serve),
        command.Run("work", "处理队列任务", bootstrap.Work),
        command.Run("schedule", "执行定时任务", bootstrap.Schedule),
        command.Run("start", "在一个进程中运行 HTTP 服务、队列和定时任务", bootstrap.Start),
        migratecmd.New(migrations.FS, postgres.Driver, dbConfig),
        restcmd.OpenAPI(bootstrap.API, bootstrap.DocRoutes),
        restcmd.Routes(bootstrap.API, bootstrap.DocRoutes),
        command.Config[settings.Config](),
        seed(),
    )
    return root
}

func dbConfig(cfg settings.Config) database.Config { return cfg.Database }
```

入口文件 `cmd/api/main.go` 只有一行：

```go
package main

import (
    "example.com/blog/internal/cli"
    "github.com/ryan053857/mygoravel/command"
)

func main() {
    command.Execute(cli.New())
}
```

`command.Execute` 执行根命令。收到 `SIGINT` 或 `SIGTERM` 时，它会取消命令的 context；命令失败时，它把错误写到标准错误，并以对应的[退出码](#exit-codes)结束进程。

`command.Root` 在根命令上定义了 `--config` 选项，所有子命令共用它。它可以传入多次，后面的文件覆盖前面的，默认是 `config/app.yaml`，见[按环境覆盖配置](configuration.md#overriding-configuration)。需要配置的命令由框架负责读取项目根目录的 [.env](configuration.md#env-files)、按 `--config` 加载配置并校验，你的代码只会拿到校验通过的配置。配置结构 `settings.Config` 见[配置](configuration.md#loading-configuration)，类型参数通常可以从你传入的函数推断出来。

| 构造函数 | 创建的命令 | 需要配置 |
| --- | --- | --- |
| `command.Run(use, short, run)` | 任意名称，例如 `serve`、`worker` | 是 |
| `command.Config[C]()` | `config check`、`config show` | 是 |
| `migratecmd.New(fsys, driver, dbConfig)` | `migrate up`、`migrate status`、`migrate down`、`migrate make` | 是 |
| `migratecmd.Publish(driver, sources...)` | 显式挂载的 `migrate publish` | 否 |
| `restcmd.OpenAPI(opts, register)` | `openapi` | 否 |
| `restcmd.Routes(opts, register)` | `routes` | 否 |

迁移命令和 HTTP API 命令分别放在 `command/migratecmd` 和 `command/restcmd` 两个子包中。`command` 本身不依赖 HTTP 和数据库组件，只导入它的应用不会链接 Huma、pgx 和 goose。

不提供 HTTP API 的应用，例如一个只消费消息队列的常驻进程，只装配它需要的命令即可：

```go
root := command.Root("worker", "订单事件消费者")
root.AddCommand(
    command.Run("run", "消费订单事件", consumer.Run),
    migratecmd.New(migrations.FS, postgres.Driver, dbConfig),
)
```

<a id="built-in-commands"></a>
## 内置命令

<a id="run-command"></a>
### Run：serve 与常驻进程

`command.Run` 创建一个先加载配置、再调用 `run(ctx, cfg)` 的命令。`ctx` 会在进程收到停止信号时被取消，常驻进程据此退出。博客应用的 `bootstrap.Serve` 装配应用并启动 HTTP 服务，见[依赖装配](dependencies.md#assembling-the-application)，HTTP 服务会据此[平滑停止](lifecycle.md)：

```bash
./bin/api serve                                                 # 使用 config/app.yaml
./bin/api serve --config config/app.yaml --config config/prod.yaml
```

`Run` 不限于常驻进程，任何“读取配置然后做一件事”的命令都可以用它创建，例如数据回填、发送一次报表。

<a id="running-together"></a>
### 在一个进程中运行多个部分

博客应用有三个常驻部分：HTTP 服务、[队列 worker](queue.md#running-workers) 和[定时任务调度器](scheduling.md#running-the-scheduler)，分别是 `App` 的 `HTTP`、`Worker` 和 `Scheduler` 方法（见[依赖装配](dependencies.md#assembling-the-application)）。每个命令只是选择运行哪些部分：

```go
// Serve 和 Start 在 app.go 中，Work 在 worker.go 中，Schedule 在 scheduler.go 中
func Serve(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).HTTP)
}

func Work(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).Worker)
}

func Schedule(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).Scheduler)
}

func Start(ctx context.Context, cfg settings.Config) error {
    return run(ctx, cfg, (*App).HTTP, (*App).Worker, (*App).Scheduler)
}
```

| 命令 | 运行的部分 |
| --- | --- |
| `serve` | HTTP 服务 |
| `work` | 队列 worker |
| `schedule` | 定时任务调度器 |
| `start` | 以上三者，在同一个进程中 |

小型部署只运行 `start`：一个进程、一份配置、一套资源。业务量增长后改为分别部署 `serve`、`work` 和 `schedule`，各自按负载增减实例，代码不用改。`start` 也可以部署多个实例：worker 竞争领取任务，调度器通过锁保证[每次触发只执行一次](scheduling.md#multiple-instances)。

`run` 用 `command.Concurrently` 同时运行这些部分：

- 收到停止信号时，各部分同时平滑停止：HTTP 服务排空请求，worker 停止领取并等待处理中的任务，调度器不再触发并等待执行中的任务。全部返回之后才关闭数据库。
- 一个部分启动失败（例如端口被占用）或出错退出时，其余部分随之停止，进程以非 0 状态退出。
- 一个部分在收到停止信号之前就正常返回，同样视为异常，错误中包含 `command.ErrStoppedEarly`，交给 systemd、Kubernetes 等重启。

各部分共用 `open` 中创建的数据库连接池，HTTP 请求和 worker 的并发任务都会占用连接，设置 [`max_open_conns`](database.md#configuration) 时要按两者之和估算。

<a id="openapi-command"></a>
### openapi 命令

`restcmd.OpenAPI(opts, register)` 以[文档模式](routing.md#exporting-openapi)和 `opts` 创建 API，调用 `register` 注册路由，然后把 OpenAPI 文档输出到标准输出：

```bash
./bin/api openapi > openapi.json
```

博客应用的 `bootstrap.API` 和 `bootstrap.DocRoutes` 定义在 `internal/bootstrap` 中，与 `serve` 共用同一份 API 选项和路由注册（见[依赖装配](dependencies.md#assembling-the-application)），因此导出的文档与实际运行的接口始终一致。文档模式不会调用处理器工厂，也不会调用 token 校验器，所以这个命令不读取配置文件，也不连接数据库。

<a id="routes-command"></a>
### routes 命令

当业务模块变多时，你可以用 `routes` 查看整个应用的路由，不必逐个打开模块中的 `routes.go`。`restcmd.Routes` 的参数与 `restcmd.OpenAPI` 相同，同样在文档模式下注册路由，不需要配置文件：

```bash
./bin/api routes
./bin/api routes --method post --path /articles
./bin/api routes --all
```

`--method` 不区分大小写，`--path` 按路径包含的文本匹配，两者同时使用时取交集。结果按路径、HTTP 方法排序，例如 `--path /articles` 会输出：

```text
METHOD  PATH                    OPERATION ID     ACCESS            SUMMARY
POST    /articles               article-create   bearer: articles  创建文章
GET     /articles/{id}          article-get      bearer: articles  获取文章
POST    /articles/{id}/publish  article-publish  bearer: articles  发布文章
```

各列的含义：

- **ACCESS** 展示路由声明的访问策略：`public` 表示显式公开，`bearer`、`session` 表示要求对应方式的认证，`bearer: articles` 同时列出所需的 scope，`docs` 表示 Huma 的文档端点。`unknown` 表示无法从声明确定策略，**不**表示允许匿名访问；
- **OPERATION ID**、**SUMMARY** 来自 `huma.Operation`，没有时显示为 `-`。摘要中的换行会合并为空格。

Huma 自动注册的 `/docs`、`/openapi.json`、`/schemas/{schema}` 等文档端点默认不显示，加上 `--all` 才会列出。列表也包含直接注册在 chi 上的原生路由和挂载的子路由；挂载的不透明 `http.Handler`（例如 `Mount("/files", handler)`）显示为 `/files/*`，无法展开其内部路由。

<a id="migrate-command"></a>
### migrate 命令

`migratecmd.New(fsys, driver, dbConfig)` 创建四个迁移命令。`fsys` 是[嵌入的迁移文件](migrations.md#embedding-migrations)，`driver` 是应用使用的数据库驱动，`dbConfig` 从应用配置中取出数据库配置：

```bash
./bin/api migrate up       # 执行全部未执行的迁移
./bin/api migrate status   # 查看每个迁移的状态
./bin/api migrate down     # 回滚最近一次迁移
go run ./cmd/api migrate make create_articles_table  # 新建迁移文件
```

`up` 和 `down` 每完成一个迁移输出一行，`status` 输出一张表：

```text
VERSION         STATE    APPLIED AT           FILE
20261111090000  applied  2026-11-11 09:12:03  20261111090000_create_articles_table.sql
20261112100000  pending  -                    20261112100000_create_article_tags_table.sql
```

`make` 按 `driver` 生成对应数据库的骨架，不加载配置、不连接数据库；其他命令会加载配置、连接数据库，并在结束后关闭连接。数据库连不上时以 `ExitUnavailable` 退出，部署脚本可以据此重试。迁移文件的编写与嵌入见[迁移](migrations.md)。

需要文件上传等功能自带的 SQL 时，可在 `migratecmd.New` 返回的命令上显式添加 `migratecmd.Publish(driver, sources...)`。其中 `driver` 是应用使用的数据库驱动，`sources` 是应用选择的 `[]migrate.Source`，例如 `migrate.Source{Name: "filesystem", FS: filesystem.Migrations()}`。注册后可以执行：

```bash
go run ./cmd/api migrate publish --dir migrations
```

这个命令只将尚未发布的 SQL 拷贝到应用目录，不加载配置、不连接数据库、不执行迁移。`--dir` 默认是 `migrations`；每个新增文件输出一行，重复执行不覆盖已有来源。注册代码、空项目首次发布方式及升级流程见[发布框架迁移](migrations.md#publishing-framework-migrations)。

<a id="config-command"></a>
### config 命令

`command.Config[C]()` 创建两个检查配置的命令，`C` 是应用的配置结构：

- `config check` 只加载并校验配置，不访问网络。发布前运行它，可以尽早发现缺失的环境变量或写错的字段；
- `config show` 输出加载后的配置，包括默认值和展开后的环境变量。默认输出 YAML，`--format json` 输出 JSON。

```bash
./bin/api config check --config config/app.yaml,config/prod.yaml
./bin/api config show --format json
```

配置文件缺失或校验失败时，这两个命令以及所有需要读取配置的命令都以 `ExitConfig` 退出。配置的读取与校验见[配置](configuration.md)。

<a id="custom-commands"></a>
## 自定义命令

根命令和内置命令都是原生的 `*cobra.Command`，任意 Cobra 命令都可以一起装配。需要读取配置时，调用 `command.LoadConfig`，它使用同一个 `--config` 选项，失败时同样带 `ExitConfig` 退出码。不接受位置参数的命令，用 `command.NoArgs` 代替 `cobra.NoArgs`，多余参数就会以 `ExitUsage` 失败：

```go
root.AddCommand(&cobra.Command{
    Use:   "ping-db",
    Short: "检查数据库能否连通",
    Args:  command.NoArgs,
    RunE: func(cmd *cobra.Command, _ []string) error {
        cfg, err := command.LoadConfig[settings.Config](cmd)
        if err != nil {
            return err
        }
        db, err := database.Open(cmd.Context(), postgres.Driver, cfg.Database)
        if err != nil {
            return command.AsExitError(command.ExitUnavailable, err)
        }
        return db.Close()
    },
})
```

选项使用 Cobra 原生的 `Flags` 定义。命令的 context 通过 `cmd.Context()` 获取，进程收到停止信号时它会被取消。

<a id="input-and-output"></a>
## 输入与输出

请遵循这个约定：**标准输出只用于命令结果，诊断信息写入标准错误**。内置命令都是这样做的，因此 `./bin/api openapi > openapi.json` 之类的重定向可以得到干净的结果；日志默认也写到标准错误（见[日志](logging.md#creating-a-logger)）。

自定义命令中，用 `cmd.OutOrStdout()` 和 `cmd.ErrOrStderr()` 写输出。这样在测试中可以用 Cobra 的 `SetOut`、`SetErr` 把输出重定向到缓冲区，见[命令测试](testing.md#command-tests)。

<a id="exit-codes"></a>
## 退出码

部署脚本和 CI 通常根据退出码判断命令是否成功，以及失败的原因：

| 退出码 | 常量 | 含义 |
| --- | --- | --- |
| 0 | | 成功 |
| 1 | `command.ExitFailure` | 操作被业务规则拒绝，例如输入无效或资源不存在 |
| 2 | `command.ExitUsage` | 用法错误，例如未知命令、未知选项或多余参数 |
| 3 | `command.ExitConfig` | 配置文件缺失或无效 |
| 4 | `command.ExitUnavailable` | 外部依赖不可用 |
| 5 | `command.ExitInternal` | 其他未分类的错误 |

用法错误、配置错误和 `migrate` 连不上数据库由框架自动标记。其他错误按[业务错误](errors.md#error-kinds)的分类转换：

| 分类 | 退出码 |
| --- | --- |
| `errorx.Unavailable` | `ExitUnavailable`（4） |
| `errorx.Internal` 及非业务错误 | `ExitInternal`（5） |
| 其余分类 | `ExitFailure`（1） |

因此，命令直接返回业务服务的错误即可。需要覆盖默认结果时，用 `command.AsExitError` 为错误标记退出码，`command.ExitCode` 则从错误中取出退出码：

```go
return command.AsExitError(command.ExitUnavailable, err)
```
