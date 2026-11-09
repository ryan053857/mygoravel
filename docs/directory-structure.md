# 目录结构

- [简介](#introduction)
- [推荐的应用结构](#application-layout)
    - [cmd 目录](#the-cmd-directory)
    - [internal/bootstrap 目录](#the-bootstrap-directory)
    - [业务包](#business-packages)
    - [migrations 目录](#the-migrations-directory)
    - [locales 目录](#the-locales-directory)
- [命名约定](#naming-conventions)
- [框架包一览](#framework-packages)

<a id="introduction"></a>
## 简介

mygoravel 不要求固定的目录结构，也不会根据目录位置自动发现任何东西。一个小型应用完全可以只有一个 `main.go`，就像[安装](installation.md)中的示例那样。

随着业务增长，你会希望把代码拆分到多个包中。本章介绍一种推荐的结构，它适合大多数中小型 API 项目。你可以从它开始，再根据团队习惯调整。

<a id="application-layout"></a>
## 推荐的应用结构

```text
my-app/
├── go.mod
├── config.yaml
├── cmd/
│   └── api/
│       └── main.go          进程入口：信号处理、命令行
├── internal/
│   ├── bootstrap/           读取配置、打开资源、组装服务
│   └── note/                一个业务模块
│       ├── note.go          模型与业务错误
│       ├── service.go       业务规则与数据库操作
│       └── notehttp/        HTTP 输入输出与路由注册
├── migrations/              SQL 迁移文件
└── locales/                 应用自己的翻译文件
```

目录不必一次建齐，等真正有代码要放时再创建即可。

<a id="the-cmd-directory"></a>
### cmd 目录

`cmd` 目录存放应用的进程入口。每个子目录对应一个可执行文件，例如 `cmd/api` 构建出 API 服务。入口文件应当尽量短小：处理系统信号、构造[命令行](commands.md)、根据退出码结束进程。

<a id="the-bootstrap-directory"></a>
### internal/bootstrap 目录

`bootstrap` 包负责把应用“组装”起来：读取配置、打开数据库连接、创建日志器、构造业务服务、注册路由。在 mygoravel 中，这些工作都由普通的 Go 代码显式完成，[依赖装配](dependencies.md)一章会详细介绍。

<a id="business-packages"></a>
### 业务包

每个业务模块放在 `internal` 下的一个独立包中，例如 `internal/note`。业务包包含模型、[业务错误](errors.md)和服务，它不关心请求来自 HTTP、命令行还是测试。

HTTP 相关的代码放在业务包的子包中，例如 `internal/note/notehttp`。它负责定义请求和响应的结构、注册路由，并在处理器中调用业务服务。依赖方向始终是 `notehttp → note`，业务包不会反过来引用 HTTP 子包。

这样划分的好处是职责清晰：

| 放在 HTTP 子包中 | 放在业务包中 |
| --- | --- |
| 请求字段的格式与校验 | 业务规则 |
| 路由路径、HTTP 方法、响应头 | 资源所有权检查 |
| 输出 DTO 的结构 | 事务边界 |
| 认证方式 | 业务错误的定义 |

<a id="the-migrations-directory"></a>
### migrations 目录

`migrations` 目录存放应用的 SQL 迁移文件。它们会被嵌入到二进制文件中，在发布时执行，详见[迁移](migrations.md)。

<a id="the-locales-directory"></a>
### locales 目录

如果你的应用需要多语言错误提示，可以在 `locales` 目录中放置 `en.yaml`、`zh-CN.yaml` 等翻译文件，详见[本地化](localization.md)。

<a id="naming-conventions"></a>
## 命名约定

遵循统一的命名约定可以让团队成员（以及客户端开发者）更容易猜到东西叫什么：

| 对象 | 约定 | 示例 |
| --- | --- | --- |
| 业务包 | 单数 | `note` |
| 资源路径 | 复数 | `/notes`、`/notes/{id}` |
| JSON 字段 | snake_case | `owner_id` |
| OperationID | `资源-动作` | `note-create` |
| 业务错误码 | `资源.原因` | `note.not_found` |
| Go 缩写 | 全大写 | `ID`、`HTTP`、`DSN`、`TTL` |

<a id="framework-packages"></a>
## 框架包一览

mygoravel 的每个包都可以单独使用，按需引入即可：

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `config` | 读取 YAML 配置、展开环境变量、校验 | [配置](configuration.md) |
| `cleanup` | 按逆序关闭入口打开的资源 | [依赖装配](dependencies.md#closing-resources) |
| `errorx` | 定义带稳定错误码和分类的业务错误 | [错误处理](errors.md) |
| `logging` | 构造 slog 日志器，传递请求关联字段 | [日志](logging.md) |
| `i18n` | 组合 go-i18n 消息目录 | [本地化](localization.md) |
| `identity` | 表示已认证的主体，并在 context 中传递 | [认证](authentication.md) |
| `httpx` | HTTP 中间件、平滑停机 | [中间件](middleware.md)、[请求生命周期](lifecycle.md) |
| `httpx/rest` | 基于 Huma 的路由注册、错误转换与 OpenAPI | [路由](routing.md) |
| `database` | 打开 PostgreSQL 连接，识别约束错误 | [数据库](database.md) |
| `database/migrate` | 构造 goose 迁移器，检查已发布迁移 | [迁移](migrations.md) |
| `database/dbtest` | 为测试创建隔离的真实数据库 | [测试](testing.md#database-tests) |
| `command` | 构造常用的 Cobra 子命令 | [命令行](commands.md) |

缓存不在框架包中，你可以直接使用 [eko/gocache](https://github.com/eko/gocache)，详见[缓存](cache.md)；数据库查询和事务也直接使用 Bun 的原生 API。
