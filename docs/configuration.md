# 配置

- [简介](#introduction)
- [加载配置](#loading-configuration)
- [环境变量](#environment-variables)
    - [.env 文件](#env-files)
- [默认值与校验](#defaults-and-validation)
- [检查配置](#inspecting-configuration)

<a id="introduction"></a>
## 简介

mygoravel 的配置是一个 YAML 文件，加载到你定义的一个 Go 结构体中。加载完成后，你拿到的是一个类型明确、已经校验过的普通结构体，可以直接传给业务服务使用。

```yaml
envfile: .env

http:
  addr: ${HTTP_ADDR:-:8080}

logs:
  level: ${LOG_LEVEL:-info}

database:
  dsn: ${DATABASE_URL}
  max_open_conns: 20
```

<a id="loading-configuration"></a>
## 加载配置

为整个配置文件定义一个根结构体，用 `yaml` 标签声明字段名。框架为常用组件预定义了配置类型，直接放进根结构体即可：

```go
package settings

import (
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/logging"
)

type Config struct {
    HTTP     httpx.Config    `yaml:"http"`
    Logs     logging.Config  `yaml:"logs"`
    Database database.Config `yaml:"database"`
}
```

然后在入口中用 `config.Load` 加载它：

```go
cfg, err := config.Load[settings.Config]("config.yaml")
```

不需要手动填写默认值：框架预定义的配置类型会在加载时自动填充自己的默认值，见[默认值](#defaults-and-validation)。

| 类型 | 配置段 | 字段说明 |
| --- | --- | --- |
| `httpx.Config` | `http` | [HTTP 配置](middleware.md#http-configuration) |
| `logging.Config` | `logs` | [创建日志器](logging.md#creating-a-logger) |
| `database.Config` | `database` | [数据库配置](database.md#configuration) |

应用自己的配置（例如业务开关、第三方服务地址）同样定义成结构体，作为新字段加入 `Config`。例如博客需要把文章中的图片上传到对象存储：

```go
type StorageConfig struct {
    Endpoint string `yaml:"endpoint"`
    Bucket   string `yaml:"bucket"`
}

type Config struct {
    HTTP     httpx.Config    `yaml:"http"`
    Logs     logging.Config  `yaml:"logs"`
    Database database.Config `yaml:"database"`
    Storage  StorageConfig   `yaml:"storage"`
}
```

```yaml
storage:
  endpoint: ${STORAGE_ENDPOINT}
  bucket: blog-images
```

`config.Load` 会依次完成：填充默认值、读取 `envfile`、展开环境变量、把文件内容解码到结构体、调用 `Validate` 校验。

解码是严格的：文件中出现结构体里没有的字段（包括拼错的段名）或类型不匹配时，会直接报错并指出行号。

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

配置类型实现 `SetDefaults()` 方法，就可以提供默认值，`Load` 会在解码前调用它：

```go
func (c *StorageConfig) SetDefaults() {
    c.Bucket = "blog-images"
}
```

文件中没有写的字段保持默认值；写了的字段覆盖默认值，包括显式写出的 `false`、`0` 和 `""`。map 按键合并，slice 整体替换。

`SetDefaults` 从内到外调用：先调用嵌套字段的，再调用外层结构体的。因此需要调整框架默认值时，在根结构体上实现 `SetDefaults` 即可：

```go
func (c *Config) SetDefaults() {
    c.HTTP.Addr = ":3000"
}
```

任何配置类型实现了 `Validate() error` 方法，`Load` 都会在解码后调用它。根结构体和所有嵌套字段都会被检查，子字段先于父结构体，所有错误一次性汇总返回：

```go
func (c StorageConfig) Validate() error {
    if c.Endpoint == "" {
        return errors.New("endpoint is required")
    }
    return nil
}
```

如果部署时忘了设置 `STORAGE_ENDPOINT`，`${STORAGE_ENDPOINT}` 会直接报错；如果设置成了空字符串，`Validate` 会拒绝它：

```text
config: config.yaml: invalid configuration: storage: endpoint is required
```

框架预定义的配置类型都已经实现了 `SetDefaults` 和 `Validate`，放进根结构体后会自动填充默认值并校验。需要跨段检查时，在根结构体上实现 `Validate` 即可。

<a id="inspecting-configuration"></a>
## 检查配置

加载后的配置可以直接编码输出，用来在部署前检查：

```go
return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg)
```

框架的 `command.NewConfig` 提供了现成的 `config check` 和 `config show` 子命令骨架，详见[命令行](commands.md)。配置检查只做加载和校验，不访问网络；"数据库能否连通"这类检查建议放在独立的 `doctor` 命令中。
