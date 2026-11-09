# 命令行

- [简介](#introduction)
- [根命令](#root-command)
- [内置命令](#built-in-commands)
    - [serve 命令](#serve-command)
    - [openapi 命令](#openapi-command)
    - [config 命令](#config-command)
    - [migrate 与 doctor 命令](#migrate-and-doctor-commands)
- [参数与选项](#arguments-and-options)
- [输入与输出](#input-and-output)
- [退出码](#exit-codes)

<a id="introduction"></a>
## 简介

一个 API 应用除了启动 HTTP 服务，通常还需要执行一些运维任务：检查配置、执行数据库迁移、导出接口文档、探测外部依赖是否可用等。mygoravel 的 `command` 包基于 [Cobra](https://github.com/spf13/cobra)，为这些常见任务提供了现成的命令骨架。

每个工厂函数返回的都是原生的 `*cobra.Command`。框架只负责命令的名称、说明和参数检查，命令具体做什么由你传入的回调决定。你可以只安装需要的命令，也可以添加完全自定义的 Cobra 命令。

完成本章后，你的应用可以这样使用：

```bash
./bin/api serve --config config.yaml
./bin/api migrate up
./bin/api openapi > openapi.json
```

<a id="root-command"></a>
## 根命令

使用 `command.Root` 创建应用的根命令，然后通过 `AddCommand` 安装子命令：

```go
root := command.Root("api", "Notes API", command.IO{
    Out: os.Stdout,
    Err: os.Stderr,
})
root.AddCommand(command.NewServe(serve))
```

在 `main` 函数中执行根命令，并把错误转换为进程退出码：

```go
func main() {
    ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
    defer stop()

    root := cli.New() // 应用自己组装根命令的函数
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
| `command.NewConfig` | `config check`、`config show` | 校验配置，输出加载后的配置 |
| `command.NewMigrate` | `migrate up`、`migrate status`、`migrate down` | 连接数据库，执行迁移 |
| `command.NewDoctor` | `doctor` | 在限定时间内探测外部依赖是否可用 |

所有回调的签名都是 `command.RunE`：

```go
type RunE func(ctx context.Context, cmd *cobra.Command, args []string) error
```

<a id="serve-command"></a>
### serve 命令

`serve` 命令的回调负责读取配置并启动服务。下面的片段假设应用已经定义了 `settings.Load` 用于加载配置，`bootstrap.Serve` 用于组装并启动应用（见[依赖装配](dependencies.md#assembling-the-application)）：

```go
root.PersistentFlags().String("config", "config.yaml", "配置文件路径")

root.AddCommand(command.NewServe(func(ctx context.Context, cmd *cobra.Command, args []string) error {
    path, _ := cmd.Flags().GetString("config")
    cfg, err := settings.Load(path)
    if err != nil {
        return command.AsExitError(command.ExitConfig, err)
    }
    return bootstrap.Serve(ctx, cfg, logger)
}))
```

<a id="openapi-command"></a>
### openapi 命令

`openapi` 命令以[文档模式](routing.md#exporting-openapi)注册路由并输出 OpenAPI 文档。由于文档模式不会调用处理器工厂，这个命令不需要配置文件，也不需要连接数据库。下面是一个完整的命令构造函数，它接收应用自己的路由注册函数：

```go
package cli

import (
    "context"
    "io"

    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/spf13/cobra"
)

func New(register func(*rest.API), out, diagnostic io.Writer) *cobra.Command {
    root := command.Root("hello", "Hello API", command.IO{
        Out: out, Err: diagnostic,
    })
    root.AddCommand(command.NewOpenAPI(func(ctx context.Context,
        cmd *cobra.Command, args []string,
    ) error {
        api := rest.New(rest.Options{Mode: rest.ModeOpenAPI})
        register(api)
        data, err := api.OpenAPIJSON()
        if err != nil {
            return err
        }
        _, err = cmd.OutOrStdout().Write(append(data, '\n'))
        return err
    }))
    return root
}
```

传入的 `register` 可以就是[路由](routing.md#basic-routing)一章中的 `registerPing`。

<a id="config-command"></a>
### config 命令

`command.NewConfig` 接收两个回调，分别对应 `config check` 和 `config show`：

```go
root.AddCommand(command.NewConfig(checkConfig, showConfig))
```

- `check` 应当加载并校验配置，只做离线检查，不访问网络；
- `show` 输出校验后的配置。它已经预定义了 `--format` 选项（默认 `yaml`），你在回调中读取这个选项并选择对应的编码器即可。

配置的读取与校验见[配置](configuration.md#inspecting-configuration)。

<a id="migrate-and-doctor-commands"></a>
### migrate 与 doctor 命令

`command.NewMigrate` 接收 `up`、`status`、`down` 三个回调，在回调中调用 goose Provider 的对应方法即可，详见[迁移](migrations.md#running-migrations)。

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
