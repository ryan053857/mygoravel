# 命令行

- [简介](#introduction)
- [根命令](#root-command)
- [内置命令](#built-in-commands)
    - [serve 命令](#serve-command)
    - [openapi 命令](#openapi-command)
    - [routes 命令](#routes-command)
    - [config 命令](#config-command)
    - [migrate 与 doctor 命令](#migrate-and-doctor-commands)
- [参数与选项](#arguments-and-options)
- [输入与输出](#input-and-output)
- [退出码](#exit-codes)

<a id="introduction"></a>
## 简介

一个 API 应用除了启动 HTTP 服务，通常还需要执行一些运维任务：检查配置、执行数据库迁移、导出接口文档、探测外部依赖是否可用等。mygoravel 的 `command` 包基于 [Cobra](https://github.com/spf13/cobra)，为这些常见任务提供了现成的命令骨架。

每个工厂函数返回的都是原生的 `*cobra.Command`。大多数命令由你传入回调执行具体工作；`routes` 接收创建 API 的回调，由框架完成路由遍历、过滤和表格输出。你可以只安装需要的命令，也可以添加完全自定义的 Cobra 命令。

完成本章后，你的应用可以这样使用：

```bash
./bin/api serve --config config.yaml
./bin/api migrate up
./bin/api openapi > openapi.json
./bin/api routes
```

<a id="root-command"></a>
## 根命令

推荐把命令行的组装放在应用的 `internal/cli` 包中，入口文件只负责处理信号和退出码。下面是博客应用完整的 `internal/cli/cli.go`，它安装了 `serve`、`openapi`、`routes`、`migrate` 和 `config` 五个命令：

```go
package cli

import (
    "context"
    "encoding/json"
    "io"
    "log/slog"

    "github.com/spf13/cobra"

    "example.com/blog/internal/auth"
    "example.com/blog/internal/bootstrap"
    "example.com/blog/internal/settings"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/config"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/logging"
)

// New 组装应用的根命令。
func New(out, errOut io.Writer) *cobra.Command {
    root := command.Root("api", "Blog API", command.IO{Out: out, Err: errOut})
    root.PersistentFlags().String("config", "config.yaml", "配置文件路径")

    root.AddCommand(command.NewServe(func(ctx context.Context, cmd *cobra.Command, _ []string) error {
        cfg, err := loadConfig(cmd)
        if err != nil {
            return err
        }
        cfg.Logs.Writer = cmd.ErrOrStderr()
        slog.SetDefault(logging.New(cfg.Logs))
        return bootstrap.Serve(ctx, cfg)
    }))

    root.AddCommand(command.NewOpenAPI(func(_ context.Context, cmd *cobra.Command, _ []string) error {
        api := rest.New(rest.Options{Title: "Blog API", Version: "1.0.0", Mode: rest.ModeOpenAPI})
        bootstrap.Routes(api, auth.Validator(auth.Config{}), nil)
        data, err := api.OpenAPIJSON()
        if err != nil {
            return err
        }
        _, err = cmd.OutOrStdout().Write(append(data, '\n'))
        return err
    }))

    root.AddCommand(command.NewRoutes(func(_ context.Context) (*rest.API, error) {
        api := rest.New(rest.Options{Title: "Blog API", Version: "1.0.0", Mode: rest.ModeOpenAPI})
        bootstrap.Routes(api, auth.Validator(auth.Config{}), nil)
        return api, nil
    }))

    root.AddCommand(newMigrateCommand())

    root.AddCommand(command.NewConfig(
        func(_ context.Context, cmd *cobra.Command, _ []string) error {
            _, err := loadConfig(cmd)
            return err
        },
        func(_ context.Context, cmd *cobra.Command, _ []string) error {
            cfg, err := loadConfig(cmd)
            if err != nil {
                return err
            }
            return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg)
        },
    ))
    return root
}

func loadConfig(cmd *cobra.Command) (settings.Config, error) {
    path, _ := cmd.Flags().GetString("config")
    cfg, err := config.Load[settings.Config](path)
    if err != nil {
        return cfg, command.AsExitError(command.ExitConfig, err)
    }
    return cfg, nil
}
```

其中 `settings.Config` 是应用的配置结构（见[配置](configuration.md#loading-configuration)），`bootstrap.Serve` 和 `bootstrap.Routes` 组装应用并注册路由（见[依赖装配](dependencies.md#assembling-the-application)），`auth.Validator` 是 token 校验器（见[认证](authentication.md#token-validators)）。

入口文件 `cmd/api/main.go` 执行根命令，并把错误转换为进程退出码：

```go
package main

import (
    "context"
    "fmt"
    "os"
    "os/signal"
    "syscall"

    "example.com/blog/internal/cli"
    "github.com/ryan053857/mygoravel/command"
)

func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    root := cli.New(os.Stdout, os.Stderr)
    if err := root.ExecuteContext(ctx); err != nil {
        fmt.Fprintln(os.Stderr, err)
        os.Exit(command.ExitCode(err))
    }
}
```

`command.Root` 创建的命令关闭了 Cobra 默认的错误输出和用法提示，所以请像上面这样自己打印错误。`ExecuteContext` 传入的 `ctx` 会传递给每个命令的回调，收到 `SIGTERM` 时它会被取消。

<a id="built-in-commands"></a>
## 内置命令

| 工厂 | 创建的命令 | 回调通常要做的事 |
| --- | --- | --- |
| `command.NewServe` | `serve` | 读取配置、打开资源、启动 HTTP 服务 |
| `command.NewOpenAPI` | `openapi` | 以文档模式注册路由，输出 OpenAPI 文档 |
| `command.NewRoutes` | `routes` | 以文档模式注册路由并返回 `*rest.API`（框架负责过滤和打印） |
| `command.NewConfig` | `config check`、`config show` | 校验配置，输出加载后的配置 |
| `command.NewMigrate` | `migrate up`、`migrate status`、`migrate down` | 连接数据库，执行迁移 |
| `command.NewDoctor` | `doctor` | 在限定时间内探测外部依赖是否可用 |

除 `NewRoutes` 外，以上命令回调的签名都是 `command.RunE`：

```go
type RunE func(ctx context.Context, cmd *cobra.Command, args []string) error
```

<a id="serve-command"></a>
### serve 命令

`serve` 的回调读取配置、安装全局日志器，然后交给 `bootstrap.Serve` 打开数据库并启动 HTTP 服务。配置加载失败时，`loadConfig` 用 `command.AsExitError` 把错误标记为 `ExitConfig`，部署脚本可以据此区分“配置写错了”和其他故障。

<a id="openapi-command"></a>
### openapi 命令

`openapi` 以[文档模式](routing.md#exporting-openapi)调用与 `serve` 相同的 `bootstrap.Routes`，因此导出的文档与实际运行的接口始终一致。文档模式不会调用处理器工厂，也不会调用 token 校验器，所以这里传入空的认证配置和 `nil` 服务即可，不需要配置文件，也不需要连接数据库：

```bash
./bin/api openapi > openapi.json
```

<a id="routes-command"></a>
### routes 命令

当业务模块变多时，你可以用 `routes` 查看整个应用的路由，不必逐个打开模块中的 `routes.go`：

```bash
./bin/api routes
./bin/api routes --method POST
./bin/api routes --path /articles
./bin/api routes --method post --path /articles
./bin/api routes --all
```

`--method` 不区分大小写，`--path` 按路径包含的文本匹配，两者同时使用时取交集。不匹配任何路由时只输出表头。结果按路径、HTTP 方法排序，例如 `--path /articles` 会输出：

```text
METHOD  PATH                    OPERATION ID     ACCESS            SUMMARY
POST    /articles               article-create   bearer: articles  创建文章
GET     /articles/{id}          article-get      bearer: articles  获取文章
POST    /articles/{id}/publish  article-publish  bearer: articles  发布文章
```

SUMMARY 来自路由声明的 `huma.Operation.Summary`，没有摘要时显示为 `-`；摘要中的换行和制表符会合并为空格，保持每条路由占一行。

`NewRoutes` 的回调签名是 `func(context.Context) (*rest.API, error)`。回调负责以 `rest.ModeOpenAPI` 创建 API（其他模式会被命令拒绝，避免执行处理器工厂），并调用与 `serve`、`openapi` 相同的 `bootstrap.Routes`；命令负责读取 `api.Routes()` 并输出结果。上面的完整示例不加载配置、不打开数据库，也不会构造 action 或执行 token 校验器。请把依赖运行时资源的 action 构造保留在[处理器工厂](routing.md#handler-factories)内。

路由声明仍留在各业务模块的 `routes.go`，新增模块时把它的 `Register` 加入 `bootstrap.Routes`，无需再维护一份总览清单。如果应用按配置启用不同路由，查看命令也应使用相同的路由选择条件。

列表包含当前 API 的原生 chi 路由和挂载的 chi 子路由。Huma 自动注册的 `/docs`、`/openapi.json`、`/schemas/{schema}` 等文档端点默认不显示，加上 `--all` 时才会列出，ACCESS 显示为 `docs`。其他没有 OpenAPI 信息的路由，其 Operation ID 和 SUMMARY 显示为 `-`，ACCESS 显示为 `unknown`。隐藏的 Huma 接口也会列出，但没有 OpenAPI 元数据可用于补充这些列。

挂载 `chi.Router` 时，列表可以展开具体的子路由。对于挂载的不透明 `http.Handler`，例如 `Mount("/files", handler)`，列表会按 chi 支持的各个方法列出 `/files/*`，包括 `CONNECT`、`QUERY` 等，ACCESS 均为 `unknown`；这些行表示挂载点接收的方法，不能据此判断处理器内部支持哪些接口，也无法展开其内部路由。

ACCESS 展示路由声明：`public` 表示显式公开，`bearer` 表示要求认证，`bearer: articles` 同时列出所需的 scope，`docs` 表示 Huma 的文档端点。`unknown` 表示无法从声明确定策略，不表示允许匿名访问；自定义中间件的实际行为不会由这个命令检查。

<a id="config-command"></a>
### config 命令

`command.NewConfig` 接收两个回调，分别对应 `config check` 和 `config show`：

- `check` 只加载并校验配置，不访问网络。发布前运行它，可以尽早发现缺失的环境变量或写错的字段；
- `show` 输出校验后的配置。上面的例子输出 JSON；`show` 还预定义了 `--format` 选项（默认 `yaml`），你可以在回调中读取它并选择对应的编码器。

配置的读取与校验见[配置](configuration.md)。

<a id="migrate-and-doctor-commands"></a>
### migrate 与 doctor 命令

`command.NewMigrate` 接收 `up`、`status`、`down` 三个回调，在回调中调用 goose Provider 的对应方法即可。上面的 `newMigrateCommand` 定义在 `internal/cli/migrate.go` 中，完整代码见[迁移命令](migrations.md#migration-commands)。

`command.NewDoctor` 用于探测外部依赖，例如数据库是否可以连通。请为每项探测设置超时，并在依赖不可用时返回 `command.ExitUnavailable` 退出码。

<a id="arguments-and-options"></a>
## 参数与选项

选项使用 Cobra 原生的 `Flags` 或 `PersistentFlags` 定义。在根命令上定义的 `PersistentFlags`（例如 `--config`）对所有子命令都可用。

内置命令都不接受位置参数，传入多余的参数会以 `ExitUsage` 退出码失败；无法识别的选项同样如此。

<a id="input-and-output"></a>
## 输入与输出

`command.IO` 绑定命令的标准输入、标准输出和标准错误。在回调中，通过 `cmd.OutOrStdout()` 和 `cmd.ErrOrStderr()` 写入输出。

请遵循这个约定：**标准输出只用于命令结果，诊断信息写入标准错误**。这样，`./bin/api openapi > openapi.json` 之类的重定向才能得到干净的结果。

在测试中，你可以把 `command.IO` 指向 `bytes.Buffer`，从而断言命令的输出，见[测试](testing.md#command-tests)。

<a id="exit-codes"></a>
## 退出码

部署脚本和 CI 通常根据退出码判断命令是否成功，以及失败的原因。`command` 包定义了以下退出码：

| 退出码 | 常量 | 含义 |
| --- | --- | --- |
| 0 | `command.ExitOK` | 成功 |
| 1 | `command.ExitFailure` | 操作被业务规则拒绝，例如输入无效或资源不存在 |
| 2 | `command.ExitUsage` | 用法错误，例如未知选项或多余参数 |
| 3 | `command.ExitConfig` | 配置错误 |
| 4 | `command.ExitUnavailable` | 外部依赖不可用 |
| 5 | `command.ExitInternal` | 其他未分类的错误 |

在回调中，用 `command.AsExitError` 为错误标记退出码：

```go
if err := db.PingContext(ctx); err != nil {
    return command.AsExitError(command.ExitUnavailable, err)
}
```

`command.ExitCode` 从错误链中取出退出码，`nil` 返回 `ExitOK`。没有显式标记的错误，按[业务错误](errors.md#error-kinds)的分类转换：

| 分类 | 退出码 |
| --- | --- |
| `errorx.Unavailable` | `ExitUnavailable`（4） |
| `errorx.Internal` 及非业务错误 | `ExitInternal`（5） |
| 其余分类 | `ExitFailure`（1） |

因此，命令回调直接返回业务服务的错误即可，不需要逐个标记。只有当你想覆盖默认结果时，才需要使用 `command.AsExitError`。
