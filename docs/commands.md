# 命令行

- [简介](#introduction)
- [声明命令行](#declaring-the-cli)
- [内置命令](#built-in-commands)
    - [serve 命令](#serve-command)
    - [openapi 命令](#openapi-command)
    - [routes 命令](#routes-command)
    - [migrate 命令](#migrate-command)
    - [config 命令](#config-command)
- [自定义命令](#custom-commands)
- [输入与输出](#input-and-output)
- [退出码](#exit-codes)

<a id="introduction"></a>
## 简介

一个 API 应用除了启动 HTTP 服务，通常还需要执行一些运维任务：检查配置、执行数据库迁移、导出接口文档、查看路由。mygoravel 的 `command` 包基于 [Cobra](https://github.com/spf13/cobra) 实现了这些命令，你只需要告诉它应用独有的部分：配置类型、路由注册函数、服务入口和迁移文件。

完成本章后，你的应用可以这样使用：

```bash
./bin/api serve
./bin/api migrate up
./bin/api openapi > openapi.json
./bin/api routes --method post
./bin/api config check --config prod.yaml
```

<a id="declaring-the-cli"></a>
## 声明命令行

下面是博客应用完整的 `internal/cli/cli.go`：

```go
package cli

import (
    "github.com/spf13/cobra"

    "example.com/blog/internal/auth"
    "example.com/blog/internal/bootstrap"
    "example.com/blog/internal/settings"
    "example.com/blog/migrations"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/httpx/rest"
)

// New 返回应用的根命令。
func New() *cobra.Command {
    return command.New(command.App[settings.Config]{
        Name:  "api",
        Short: "Blog API",

        API: rest.Options{Title: "Blog API", Version: "1.0.0"},
        Routes: func(api *rest.API) {
            bootstrap.Routes(api, auth.Validator(auth.Config{}), nil)
        },

        Serve: bootstrap.Serve,

        Migrations: migrations.FS,
        Database:   func(cfg settings.Config) database.Config { return cfg.Database },
    })
}
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

`App` 的类型参数是应用的配置结构（见[配置](configuration.md#loading-configuration)）。各字段的作用如下，字段为空时不安装对应的命令：

| 字段 | 作用 | 安装的命令 |
| --- | --- | --- |
| `Name`、`Short` | 可执行文件名和一句话描述，显示在帮助中 | |
| `ConfigFile` | `--config` 选项的默认值，为空时是 `config.yaml` | `config check`、`config show`（总是安装） |
| `API`、`Routes` | 创建 API 的选项，以及注册全部路由的函数 | `openapi`、`routes` |
| `Serve` | 打开资源并启动 HTTP 服务 | `serve` |
| `Migrations`、`Database` | 嵌入的迁移文件，以及从配置中取出数据库配置的函数 | `migrate up`、`migrate status`、`migrate down` |

`--config` 是根命令上的选项，所有子命令都可以使用。框架负责读取它、调用 `config.Load` 并校验配置，你的代码只会拿到校验通过的配置。

<a id="built-in-commands"></a>
## 内置命令

<a id="serve-command"></a>
### serve 命令

`serve` 加载配置，然后调用 `Serve(ctx, cfg)`。这里的 `bootstrap.Serve` 负责安装日志器、打开数据库并启动 HTTP 服务，见[依赖装配](dependencies.md#assembling-the-application)。`ctx` 会在进程收到停止信号时被取消，HTTP 服务据此[平滑停止](lifecycle.md)。

```bash
./bin/api serve --config config.yaml
```

<a id="openapi-command"></a>
### openapi 命令

`openapi` 以[文档模式](routing.md#exporting-openapi)创建 API，调用 `Routes` 注册路由，然后把 OpenAPI 文档输出到标准输出：

```bash
./bin/api openapi > openapi.json
```

`Routes` 和 `serve` 调用的是同一个 `bootstrap.Routes`，因此导出的文档与实际运行的接口始终一致。文档模式不会调用处理器工厂，也不会调用 token 校验器，所以上面传入空的认证配置和 `nil` 服务即可。这个命令不读取配置文件，也不连接数据库。

<a id="routes-command"></a>
### routes 命令

当业务模块变多时，你可以用 `routes` 查看整个应用的路由，不必逐个打开模块中的 `routes.go`。它和 `openapi` 一样在文档模式下注册路由，不需要配置文件：

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

- **ACCESS** 展示路由声明的访问策略：`public` 表示显式公开，`bearer` 表示要求认证，`bearer: articles` 同时列出所需的 scope，`docs` 表示 Huma 的文档端点。`unknown` 表示无法从声明确定策略，**不**表示允许匿名访问；
- **OPERATION ID**、**SUMMARY** 来自 `huma.Operation`，没有时显示为 `-`。摘要中的换行会合并为空格。

Huma 自动注册的 `/docs`、`/openapi.json`、`/schemas/{schema}` 等文档端点默认不显示，加上 `--all` 才会列出。列表也包含直接注册在 chi 上的原生路由和挂载的子路由；挂载的不透明 `http.Handler`（例如 `Mount("/files", handler)`）显示为 `/files/*`，无法展开其内部路由。

<a id="migrate-command"></a>
### migrate 命令

设置 `Migrations` 和 `Database` 后，应用获得三个迁移命令：

```bash
./bin/api migrate up       # 执行全部未执行的迁移
./bin/api migrate status   # 查看每个迁移的状态
./bin/api migrate down     # 回滚最近一次迁移
```

`up` 和 `down` 每完成一个迁移输出一行，`status` 输出一张表：

```text
VERSION         STATE    APPLIED AT           FILE
20261111090000  applied  2026-11-11 09:12:03  20261111090000_create_articles.sql
20261112100000  pending  -                    20261112100000_add_article_tags.sql
```

命令会加载配置、连接数据库并在结束后关闭连接。数据库连不上时以 `ExitUnavailable` 退出，部署脚本可以据此重试。迁移文件的编写与嵌入见[迁移](migrations.md)。

<a id="config-command"></a>
### config 命令

- `config check` 只加载并校验配置，不访问网络。发布前运行它，可以尽早发现缺失的环境变量或写错的字段；
- `config show` 输出加载后的配置，包括默认值和展开后的环境变量。默认输出 YAML，`--format json` 输出 JSON。

```bash
./bin/api config check --config prod.yaml
./bin/api config show --format json
```

配置文件缺失或校验失败时，这两个命令以及所有需要读取配置的命令都以 `ExitConfig` 退出。配置的读取与校验见[配置](configuration.md)。

<a id="custom-commands"></a>
## 自定义命令

`command.New` 返回的是原生的 `*cobra.Command`，可以继续添加自定义命令。需要读取配置时，调用 `command.LoadConfig`，它使用同一个 `--config` 选项，失败时同样带 `ExitConfig` 退出码：

```go
root := command.New(command.App[settings.Config]{ /* ... */ })

root.AddCommand(&cobra.Command{
    Use:   "ping-db",
    Short: "检查数据库能否连通",
    Args:  cobra.NoArgs,
    RunE: func(cmd *cobra.Command, _ []string) error {
        cfg, err := command.LoadConfig[settings.Config](cmd)
        if err != nil {
            return err
        }
        db, err := database.Open(cmd.Context(), cfg.Database)
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
