# 配置

- [简介](#introduction)
- [读取配置](#reading-configuration)
    - [定义配置结构](#defining-configuration)
    - [从文件加载](#loading-from-files)
    - [从字节加载](#loading-from-bytes)
- [环境变量](#environment-variables)
    - [引用语法](#reference-syntax)
    - [.env 文件](#env-files)
    - [在测试中提供环境变量](#environment-in-tests)
- [默认值与覆盖规则](#defaults-and-overrides)
- [按需加载配置段](#loading-sections)
- [敏感值](#secrets)
- [检查与输出配置](#inspecting-configuration)

<a id="introduction"></a>
## 简介

mygoravel 的配置由两部分组成：你在 Go 代码中定义的**配置结构体**，以及一个 **YAML 配置文件**。加载过程分三步：

1. 在结构体中写好默认值；
2. 把 YAML 文件中对应的配置段解码到结构体，覆盖默认值；
3. 调用结构体的 `Validate` 方法进行校验。

校验通过后，你得到的是一个类型明确、值已确认有效的普通 Go 结构体，可以直接传给业务服务使用。业务代码不需要关心配置来自文件、环境变量还是测试代码。

一个典型的配置文件如下所示。除保留键 `envfile` 外，每个顶层键称为一个**配置段**：

```yaml
envfile: .env

http:
  addr: ${HTTP_ADDR:-:8080}

database:
  dsn: ${DATABASE_URL}
  max_open_conns: 20
```

<a id="reading-configuration"></a>
## 读取配置

<a id="defining-configuration"></a>
### 定义配置结构

为每个配置段定义一个结构体，用 `yaml` 标签声明字段名。如果结构体实现了 `Validate() error` 方法（即 `config.Validator` 接口），解码成功后会自动调用它：

```go
type HTTPConfig struct {
    Addr string `yaml:"addr"`
}

func (c HTTPConfig) Validate() error {
    if c.Addr == "" {
        return errors.New("http.addr is required")
    }
    return nil
}
```

<a id="loading-from-files"></a>
### 从文件加载

下面是一个完整的配置读取函数，它从文件中读取 `http` 段：

```go
package settings

import (
    "github.com/cockroachdb/errors"
    "github.com/ryan053857/mygoravel/config"
)

type HTTPConfig struct {
    Addr string `yaml:"addr"`
}

func (c HTTPConfig) Validate() error {
    if c.Addr == "" {
        return errors.New("http.addr is required")
    }
    return nil
}

func LoadHTTP(path string) (HTTPConfig, error) {
    cfg := HTTPConfig{Addr: ":8080"}
    doc, err := config.ReadFile(path, config.WithSections("http"))
    if err != nil {
        return cfg, err
    }
    err = doc.Decode("http", &cfg)
    return cfg, err
}
```

`config.ReadFile` 负责读取并解析文件。`config.WithSections` 声明文件中**允许出现**的顶层配置段，这个选项是必需的。文件中出现未声明的段时会直接报错，这样拼写错误不会被悄悄忽略；`Decode` 也只能读取已声明的段。

`doc.Decode` 把指定配置段解码到结构体中。如果文件里没有这个段，结构体会保持原来的默认值。

<a id="loading-from-bytes"></a>
### 从字节加载

如果配置内容已经在内存中（例如来自嵌入文件或测试数据），可以使用 `config.Parse` 代替 `config.ReadFile`，它接受相同的选项：

```go
doc, err := config.Parse(data, config.WithSections("http"))
```

<a id="environment-variables"></a>
## 环境变量

<a id="reference-syntax"></a>
### 引用语法

配置文件中的值可以引用环境变量。这让你可以把同一份配置文件用于不同的部署环境，同时避免把密码写进文件：

| 写法 | 行为 |
| --- | --- |
| `${NAME}` | 变量必须已设置，否则报错；设置为空字符串也算已设置 |
| `${NAME:-fallback}` | 变量未设置或为空时，使用 `fallback` |
| `${NAME:-}` | 允许变量未设置，此时使用空字符串 |
| `$${NAME}` | 转义，输出字面量 `${NAME}` |

变量默认从进程环境中读取，如果配置文件声明了 `envfile`，还会从该文件中读取，见 [.env 文件](#env-files)。

环境变量在 YAML 解析**之后**按标量值展开，因此变量内容不会改变文件的结构。展开只发生在 `Decode` 所选的配置段内。

<a id="env-files"></a>
### .env 文件

在配置文件顶层写上 `envfile`，就可以从一个 `.env` 文件中补充环境变量：

```yaml
envfile: env/.env

logs:
  level: ${LOG_LEVEL:-info}
```

```ini
# env/.env
LOG_LEVEL=debug
export DATABASE_URL="postgres://app:secret@localhost:5432/app"
```

`envfile` 遵循以下规则：

- **进程环境优先。** 查找变量时先查进程环境，找不到才查 `.env` 文件。进程中设为空字符串的变量也算已设置，同样优先。因此部署时直接用环境变量覆盖 `.env` 中的值即可。
- **不修改进程环境。** `.env` 中的变量只用于展开当前配置文件，不会写入 `os.Environ`。
- **文件必须存在。** 声明了 `envfile` 但文件不存在、路径为空或语法错误时，`ReadFile` 和 `Parse` 都会直接报错。不需要 `.env` 的环境（例如生产容器），不要写 `envfile` 这一行。
- **相对路径基于配置文件所在目录**，而不是当前工作目录；使用 `config.Parse` 时没有文件位置，相对路径基于当前工作目录。
- **路径本身可以引用进程环境变量**，例如 `envfile: env/${APP_ENV}.env`。
- `envfile` 是保留键，不能在 `WithSections` 中声明，也不能用 `Decode` 读取。

`.env` 文件由 [godotenv](https://github.com/joho/godotenv) 解析，支持 `#` 注释、`export` 前缀、单引号和双引号。未加引号或双引号中的 `$NAME` / `${NAME}` 会被替换为**同一文件中前面定义的**变量，如果值中包含 `$`（例如密码），请用单引号包起来：

```ini
DB_PASSWORD='s3cr$ET'
```

> [!NOTE]
> 语法错误的报错只包含文件路径，不会回显文件内容，以免泄露其中的密钥。

<a id="environment-in-tests"></a>
### 在测试中提供环境变量

`config.WithLookup` 可以替换默认的进程环境查找。测试时不必修改真实的进程环境，只需传入一个基于 map 的查询函数：

```go
lookup := func(name string) (string, bool) {
    value, ok := map[string]string{"HTTP_ADDR": ":9000"}[name]
    return value, ok
}

doc, err := config.Parse(data,
    config.WithSections("http"),
    config.WithLookup(lookup),
)
```

每个测试持有自己的查询函数，因此可以安全地并行运行。如果配置文件声明了 `envfile`，`.env` 中的变量仍会作为查询函数未命中时的后备。

<a id="defaults-and-overrides"></a>
## 默认值与覆盖规则

配置文件中没有写的字段会保留结构体中的默认值。合并时遵循以下规则：

- 文件中**显式写出**的 `false`、`0`、`""` 和 `[]` 会覆盖默认值。这些值是否合法，由你的 `Validate` 方法决定。
- map 按键合并；写成 `{}` 时保留默认 map 中已有的键。
- slice 整体替换，而不是追加。

> [!TIP]
> 对于“允许的凭据列表”这类配置，建议默认值使用空 slice，让配置文件完整地定义当前生效的列表。这样就不会出现默认值和文件内容混在一起的情况。

为了尽早发现配置错误，以下情况都会直接报错：显式的 `null`、未声明的顶层配置段、重复的键、结构体中不存在的字段、类型不匹配，以及一个文件中包含多个 YAML 文档。

配置加载完成后，应当把它当作不可变的数据使用。

<a id="loading-sections"></a>
## 按需加载配置段

一个应用通常会有多个命令，而每个命令只需要一部分配置。例如，执行数据库迁移时不需要 HTTP 或缓存的配置。你可以在 `WithSections` 中声明文件中所有合法的段，然后只解码当前命令需要的那一段：

```go
doc, err := config.ReadFile(path,
    config.WithSections("http", "database", "auth", "cache"),
)
if err != nil {
    return err
}

cfg := database.DefaultConfig()
if err := doc.Decode("database", &cfg); err != nil {
    return err
}
```

这里的 `database` 是框架的 `github.com/ryan053857/mygoravel/database` 包。这样做的好处是：迁移命令只要求 `DATABASE_URL` 存在，而不会因为缺少某个 HTTP 相关的环境变量而失败。与此同时，未知配置段、重复键和 `null` 仍然会在解析整个文件时被检查出来。

如果你要实现一个“检查全部配置”的命令，可以依次解码每个配置段，并用 `errors.Join` 汇总所有错误，一次性报告给用户。

<a id="secrets"></a>
## 敏感值

密码、token、DSN 等字段应声明为 `config.Secret` 类型：

```go
type Credentials struct {
    Token config.Secret `yaml:"token" json:"token"`
}
```

`config.Secret` 在被打印或序列化时会自动脱敏：非空值在 `fmt`、`slog`、JSON 和 YAML 输出中都显示为 `******`，空值则显示为空，方便你区分“未设置”和“已设置”。

真正需要使用原始值时（例如连接数据库或比对 token），调用 `Reveal` 方法：

```go
client := newClient(creds.Token.Reveal())
```

> [!WARNING]
> `Reveal()` 返回的是普通字符串，不再受脱敏保护。请只在真正需要原值的地方调用它，不要把它的结果写进日志或错误消息。

<a id="inspecting-configuration"></a>
## 检查与输出配置

在部署前检查配置是否正确是一个好习惯。由于 `config.Secret` 会在序列化时自动脱敏，你可以放心地把校验后的配置直接编码输出：

```go
return json.NewEncoder(cmd.OutOrStdout()).Encode(cfg)
```

这段代码通常写在一个 Cobra 命令的回调中：`cmd` 是当前命令，`cfg` 是你的完整配置结构体。框架的 `command.NewConfig` 提供了现成的 `config check` 和 `config show` 子命令骨架，详见[命令行](commands.md)。

配置检查应当只做解码和结构校验，不访问网络。像“数据库能否连通”这样的检查，建议放在独立的 `doctor` 命令中，并为它设置超时。
