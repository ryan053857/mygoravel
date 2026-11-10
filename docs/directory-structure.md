# 目录结构

- [简介](#introduction)
- [推荐的应用结构](#application-layout)
    - [cmd 目录](#the-cmd-directory)
    - [internal/bootstrap 目录](#the-bootstrap-directory)
    - [业务包](#business-packages)
    - [按业务动作组织处理器](#http-actions)
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

下面是文档中贯穿使用的博客应用的结构：

```text
blog/
├── go.mod
├── .env                     本地开发用的环境变量（不提交）
├── config/
│   ├── app.yaml             基础配置
│   └── prod.yaml            生产环境的差异
├── cmd/
│   └── api/
│       └── main.go          进程入口：执行命令行
├── internal/
│   ├── cli/                 装配命令行：serve、routes、openapi、migrate、config
│   ├── settings/            配置结构与加载
│   ├── bootstrap/           打开资源、组装服务、注册路由
│   ├── auth/                token 校验器
│   └── article/             文章业务模块
│       ├── article.go       模型与业务错误
│       ├── service.go       业务规则与数据库操作
│       └── articlehttp/     HTTP 输入输出与路由注册
│           ├── routes.go                本模块的路由声明与 action 绑定
│           └── actions/                 HTTP 请求处理动作
│               ├── create_article.go    创建文章
│               ├── get_article.go       获取文章
│               ├── publish_article.go   发布文章
│               └── dto.go               动作共享的响应结构与转换
├── migrations/              SQL 迁移文件和 embed.go
└── locales/                 应用自己的翻译文件
```

各个文件的完整代码分布在相应的章节中：

| 文件 | 章节 |
| --- | --- |
| `cmd/api/main.go`、`internal/cli/cli.go` | [命令行](commands.md#declaring-the-cli) |
| `config/`、`.env`、`internal/settings` | [配置](configuration.md) |
| `internal/bootstrap/serve.go` | [依赖装配](dependencies.md#assembling-the-application) |
| `internal/auth/auth.go` | [认证](authentication.md#token-validators) |
| `internal/article/article.go`、`service.go` | [数据库](database.md#defining-models)、[错误处理](errors.md#example)、[授权](authorization.md#checking-in-transactions) |
| `internal/article/articlehttp/routes.go` | [模块内注册](routing.md#module-registration) |
| `internal/article/articlehttp/actions/` 下的 `create_article.go`、`get_article.go`、`publish_article.go` | [按业务动作组织处理器](routing.md#http-actions) |
| `internal/article/articlehttp/actions/dto.go` | [共享响应结构](routing.md#shared-responses) |
| `migrations/` | [迁移](migrations.md#embedding-migrations) |

目录不必一次建齐，等真正有代码要放时再创建即可。

<a id="the-cmd-directory"></a>
### cmd 目录

`cmd` 目录存放应用的进程入口。每个子目录对应一个可执行文件，例如 `cmd/api` 构建出 API 服务。入口文件应当尽量短小，通常只有一行 `command.Execute(cli.New())`，见[命令行](commands.md#declaring-the-cli)。

<a id="the-bootstrap-directory"></a>
### internal/bootstrap 目录

`bootstrap` 包负责把应用“组装”起来：安装全局日志器、打开数据库连接、构造业务服务、注册路由。在 mygoravel 中，这些工作都由普通的 Go 代码显式完成，[依赖装配](dependencies.md)一章会详细介绍。

<a id="business-packages"></a>
### 业务包

每个业务模块放在 `internal` 下的一个独立包中，例如 `internal/article`。业务包包含模型、[业务错误](errors.md)和服务，它不关心请求来自 HTTP、命令行还是测试。

HTTP 相关的代码放在业务包的子包中，例如 `internal/article/articlehttp`。其中 `routes.go` 注册路由，`actions` 子包定义请求和响应的结构，并在处理器中调用业务服务。依赖方向是 `articlehttp → actions → article`；路由入口也可以接收业务服务并传给 action，但业务包不会反过来引用 HTTP 层。

这样划分的好处是职责清晰：

| 放在 HTTP 子包中 | 放在业务包中 |
| --- | --- |
| 请求字段的格式与校验 | 业务规则 |
| 路由路径、HTTP 方法、响应头 | 资源所有权检查 |
| 输出 DTO 的结构 | 事务边界 |
| 认证方式 | 业务错误的定义 |

<a id="http-actions"></a>
### 按业务动作组织处理器

文档推荐在模块的 HTTP 包内使用 `actions/` 目录，让你从目录树就能识别请求处理代码。每个 action 单独放在一个文件中，包含该动作的输入、独有的输出、依赖、构造函数和 `Handle` 方法。比如 `actions/create_article.go` 定义 `CreateArticle`，`actions/publish_article.go` 定义 `PublishArticle`。

模块的 `routes.go` 属于 `articlehttp` 包，统一声明路径、HTTP 方法、访问策略和 action 绑定；`actions/` 是独立的 Go 子包，所有动作和共享响应文件 `actions/dto.go` 都放在这个包中。完整代码见[组织路由](routing.md#organizing-routes)。

目录名表达职责，文件名和类型名表达业务动作。例如，“归档文章”使用 `actions/archive_article.go` 和 `actions.ArchiveArticle`，无需再给文件名加 `action_` 前缀或给类型名加 `Action` 后缀。把 `actions` 放在 HTTP 包内，也能说明它们负责 HTTP 请求适配，业务规则仍然放在 `article` 服务中。

共享 DTO 放在 `actions` 包内，可以避免处理器反过来导入路由包而形成循环依赖。无需为每个 action 再创建子包，也无需提前拆出更多只有少量类型的包。

这些是推荐的组织与命名约定，不是框架限制。mygoravel 不会扫描目录、检查一个文件里有几个 action，也不要求实现 `Action` 接口；你可以根据应用规模使用普通函数或其他布局。业务服务同样无需按每个 HTTP 动作拆成独立类型。

查看应用全部路由时，可以使用 [routes 命令](commands.md#routes-command)。它从同一份注册代码生成全局路由表，模块内部仍然保留自己的 `routes.go`。

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
| 业务包 | 单数 | `article` |
| HTTP action 目录 | HTTP 包内的 `actions/` | `articlehttp/actions/` |
| action 文件 | `动作_业务对象.go` | `actions/create_article.go`、`actions/publish_article.go` |
| action 类型 | `动作 + 业务对象` | `CreateArticle`、`PublishArticle` |
| action 构造函数 | `New` + 类型名 | `NewCreateArticle` |
| action 处理方法 | `Handle` | `(*CreateArticle).Handle` |
| 输入、独有输出 | 动作名 + `Input` / `Output` | `CreateArticleInput` |
| 资源路径 | 复数 | `/articles`、`/articles/{id}` |
| JSON 字段 | snake_case | `author_id` |
| OperationID | `资源-动作` | `article-create` |
| 业务错误码 | `资源.原因` | `article.not_found` |
| Go 缩写 | 全大写 | `ID`、`HTTP`、`DSN`、`TTL` |

<a id="framework-packages"></a>
## 框架包一览

mygoravel 的每个包都可以单独使用，按需引入即可：

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `config` | 读取 YAML 配置、展开环境变量、校验 | [配置](configuration.md) |
| `cleanup` | 按逆序关闭入口打开的资源 | [依赖装配](dependencies.md#closing-resources) |
| `errorx` | 定义带稳定错误码和分类的业务错误 | [错误处理](errors.md) |
| `logging` | 构造 slog 日志器，传递请求关联字段；`logging/logtest` 用于测试 | [日志](logging.md) |
| `i18n` | 组合 go-i18n 消息目录 | [本地化](localization.md) |
| `identity` | 表示已认证的主体，并在 context 中传递 | [认证](authentication.md) |
| `page` | 固定分页与游标分页的参数和响应结构 | [分页](responses.md#pagination) |
| `httpx` | HTTP 中间件、平滑停机 | [中间件](middleware.md)、[请求生命周期](lifecycle.md) |
| `httpx/rest` | 基于 Huma 的路由注册、错误转换与 OpenAPI | [路由](routing.md) |
| `database` | 打开 PostgreSQL 连接，识别约束错误 | [数据库](database.md) |
| `database/migrate` | 构造 goose 迁移器、发布功能包迁移、检查历史迁移 | [迁移](migrations.md) |
| `database/dbtest` | 为测试创建隔离的真实数据库 | [测试](testing.md#database-tests) |
| `filesystem` | 基于单张 `files` 表管理上传、确认、公开地址和清理 | [文件存储与上传](filesystem.md) |
| `filesystem/local`、`filesystem/s3` | 本地与 S3 磁盘 | [文件存储与上传](filesystem.md#disks) |
| `command` | 根命令、`Run`、`config` 命令、退出码 | [命令行](commands.md) |
| `command/migratecmd` | `migrate` 命令 | [migrate 命令](commands.md#migrate-command) |
| `command/restcmd` | `openapi`、`routes` 命令 | [openapi 命令](commands.md#openapi-command) |

缓存不在框架包中，你可以直接使用 [eko/gocache](https://github.com/eko/gocache)，详见[缓存](cache.md)；数据库查询和事务也直接使用 Bun 的原生 API。
