# 配置

- [简介](#introduction)
- [加载配置](#loading-configuration)
- [环境变量](#environment-variables)
    - [.env 文件](#env-files)
- [默认值与校验](#defaults-and-validation)
- [敏感值](#secrets)
- [检查配置](#inspecting-configuration)

<a id="introduction"></a>
## 简介

mygoravel 的配置是一个 YAML 文件，加载到你定义的一个 Go 结构体中。加载完成后，你拿到的是一个类型明确、已经校验过的普通结构体，可以直接传给业务服务使用。

```yaml
envfile: .env

http:
  addr: ${HTTP_ADDR:-:8080}

database:
  dsn: ${DATABASE_URL}
  max_open_conns: 20
```

<a id="loading-configuration"></a>
## 加载配置

为整个配置文件定义一个根结构体，用 `yaml` 标签声明字段名，在结构体中写好默认值，然后调用 `config.Load`：

```go
package settings

import (
    "github.com/ryan053857/mygoravel/config"
    "github.com/ryan053857/mygoravel/database"
)

type Config struct {
    HTTP     HTTPConfig      `yaml:"http"`
    Database database.Config `yaml:"database"`
}

type HTTPConfig struct {
    Addr string `yaml:"addr"`
}

func Load(path string) (Config, error) {
    cfg := Config{
        HTTP:     HTTPConfig{Addr: ":8080"},
        Database: database.DefaultConfig(),
    }
    err := config.Load(path, &cfg)
    return cfg, err
}
```

`config.Load` 会依次完成：读取 `envfile`、展开环境变量、把文件内容解码到结构体、调用 `Validate` 校验。

解码是严格的：文件中出现结构体里没有的字段（包括拼错的段名）、类型不匹配或者显式的 `null`，都会直接报错并指出字段路径和行号，错误信息不会包含配置值本身。

<a id="environment-variables"></a>
## 环境变量

配置文件中的值可以引用进程环境变量：

| 写法 | 行为 |
| --- | --- |
| `${NAME}` | 变量必须已设置，否则报错；设置为空字符串也算已设置 |
| `${NAME:-fallback}` | 变量未设置或为空时，使用 `fallback` |
| `${NAME:-}` | 允许变量未设置，此时使用空字符串 |
| `$${NAME}` | 转义，输出字面量 `${NAME}` |

变量在 YAML 解析之后才展开，所以变量的内容只会成为字段的值，不会改变文件的结构。

<a id="env-files"></a>
### .env 文件

在配置文件顶层写上 `envfile`，可以从一个 `.env` 文件中补充环境变量：

```yaml
envfile: env/.env
```

```ini
# env/.env
LOG_LEVEL=debug
DATABASE_URL=postgres://app:secret@localhost:5432/app
```

- **进程环境优先**：进程中已经设置的变量（包括空字符串）不会被 `.env` 覆盖。部署时直接用环境变量覆盖即可。
- **文件必须存在**：文件不存在或者语法错误时都会报错。生产环境不需要 `.env` 的话，就不要写 `envfile`。
- **相对路径基于配置文件所在目录**；路径中可以引用环境变量，例如 `env/${APP_ENV}.env`。

`.env` 由 [godotenv](https://github.com/joho/godotenv) 解析。值中如果有 `$`（例如密码），请用单引号包起来，否则会被当成变量引用：`DB_PASSWORD='s3cr$ET'`。

<a id="defaults-and-validation"></a>
## 默认值与校验

调用 `Load` 前在结构体中写好的值就是默认值。文件中没有写的字段保持默认值；写了的字段覆盖默认值，包括显式写出的 `false`、`0` 和 `""`。map 按键合并，slice 整体替换。

任何配置类型实现了 `Validate() error` 方法，`Load` 都会在解码后调用它。根结构体和所有嵌套字段都会被检查，子字段先于父结构体，所有错误一次性汇总返回：

```go
func (c HTTPConfig) Validate() error {
    if c.Addr == "" {
        return errors.New("addr is required")
    }
    return nil
}
```

```text
config: config.yaml: invalid configuration: http: addr is required
```

框架自带的配置类型（例如 `database.Config`）已经实现了 `Validate`，嵌入根结构体后会自动校验。需要跨段检查时，在根结构体上实现 `Validate` 即可。

<a id="secrets"></a>
## 敏感值

密码、token、DSN 等字段应声明为 `config.Secret` 类型。它在 `fmt`、`slog`、JSON 和 YAML 输出中都显示为 `******`，空值显示为空。真正需要原始值时调用 `Reveal`：

```go
type Credentials struct {
    Token config.Secret `yaml:"token"`
}

client := newClient(creds.Token.Reveal())
```

> [!WARNING]
> `Reveal()` 返回的是普通字符串，不再受脱敏保护，不要把它写进日志或错误消息。

<a id="inspecting-configuration"></a>
## 检查配置

由于 `config.Secret` 会自动脱敏，加载后的配置可以直接编码输出，用来在部署前检查：

```go
return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg)
```

框架的 `command.NewConfig` 提供了现成的 `config check` 和 `config show` 子命令骨架，详见[命令行](commands.md)。配置检查只做加载和校验，不访问网络；"数据库能否连通"这类检查建议放在独立的 `doctor` 命令中。
