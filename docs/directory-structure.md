# 目录结构

- [简介](#introduction)
- [推荐的应用结构](#application-layout)
    - [cmd 目录](#the-cmd-directory)
    - [internal/bootstrap 目录](#the-bootstrap-directory)
    - [业务包](#business-packages)
    - [按业务动作组织代码](#http-actions)
    - [migrations 目录](#the-migrations-directory)
    - [locales 目录](#the-locales-directory)
- [命名约定](#naming-conventions)
- [框架包一览](#framework-packages)
    - [基础功能](#foundation)
    - [深入了解](#digging-deeper)
    - [应用功能](#application-features)
    - [AI](#ai-packages)
    - [安全](#security-packages)
    - [数据库](#database-packages)

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
│   ├── cli/                 装配命令行：serve、work、schedule、start、routes、openapi、migrate、config，以及 bootstrap.Commands
│   ├── settings/            配置结构与加载
│   ├── bootstrap/           打开资源、组装服务；HTTP 服务、队列 worker、调度器等运行部分，以及需要 App 的命令（各模块命令、seed、queue）
│   ├── auth/                token 校验器；authweb/ 是登录页
│   ├── web/                 页面布局（templ）、Tailwind 的样式入口 app.css；static/ 是嵌入的静态文件
│   └── article/             文章业务模块
│       ├── article.go       模型、业务错误与共享的规则
│       ├── create.go        创建文章
│       ├── get.go           获取文章
│       ├── update.go        修改文章
│       ├── publish.go       发布文章：HTTP 接口、队列任务与命令行
│       ├── dto.go           共享的响应结构与转换
│       ├── routes.go        本模块的路由声明
│       ├── commands.go      本模块的命令声明
│       └── articleweb/      编辑后台页面
│           ├── routes.go    页面路由
│           ├── handlers.go  页面处理器
│           └── views/       templ 组件及生成的 _templ.go
├── migrations/              SQL 迁移文件和 embed.go
└── locales/                 应用自己的翻译文件
```

各个文件的完整代码分布在相应的章节中：

| 文件 | 章节 |
| --- | --- |
| `cmd/api/main.go`、`internal/cli/cli.go` | [命令行](commands.md#declaring-the-cli) |
| `internal/bootstrap/seed.go` | [填充数据](migrations.md#seeding) |
| `config/`、`.env`、`internal/settings` | [配置](configuration.md) |
| `internal/bootstrap/app.go`、`http.go` | [依赖装配](dependencies.md#assembling-the-application) |
| `internal/bootstrap/worker.go` | [队列](queue.md#running-workers) |
| `internal/bootstrap/scheduler.go` | [定时任务](scheduling.md#running-the-scheduler) |
| `internal/bootstrap/commands.go`、`internal/article/commands.go` | [从命令行运行 action](actions.md#as-commands) |
| `internal/auth/auth.go` | [认证](authentication.md#token-validators) |
| `internal/article/article.go` | [数据库](database.md#defining-models)、[模块内共享的代码](actions.md#shared-code) |
| `internal/article/publish.go` | [编写 action](actions.md#writing-actions)、[授权](authorization.md#checking-in-transactions) |
| `internal/article/create.go`、`get.go`、`update.go` | [数据库](database.md#constraint-errors)、[action 的 HTTP 入口](routing.md#http-actions)、[错误处理](errors.md#example) |
| `internal/article/routes.go`、`dto.go` | [模块内注册](routing.md#module-registration)、[共享响应结构](routing.md#shared-responses) |
| `internal/web/`、`internal/article/articleweb/` | [视图](views.md)、[表单](forms.md)、[前端资源](assets.md)、[界面组件](components.md) |
| `internal/auth/authweb/` | [登录](views.md#login) |
| `migrations/` | [迁移](migrations.md#embedding-migrations) |

目录不必一次建齐，等真正有代码要放时再创建即可。

<a id="the-cmd-directory"></a>
### cmd 目录

`cmd` 目录存放应用的进程入口。每个子目录对应一个可执行文件，例如 `cmd/api` 构建出 API 服务。入口文件应当尽量短小，通常只有一行 `command.Execute(cli.New())`，见[命令行](commands.md#declaring-the-cli)。

<a id="the-bootstrap-directory"></a>
### internal/bootstrap 目录

`bootstrap` 包负责把应用“组装”起来：安装全局日志器、打开数据库连接、注册路由、启动队列和调度器。在 mygoravel 中，这些工作都由普通的 Go 代码显式完成，[依赖装配](dependencies.md)一章会详细介绍。

<a id="business-packages"></a>
### 业务包

每个业务模块放在 `internal` 下的一个独立包中，例如 `internal/article`。业务包由 [action](actions.md) 组成：每个业务动作一个文件，包含它的依赖、业务规则，以及 HTTP 接口、队列任务等入口。多个 action 共用的模型、[业务错误](errors.md)和授权函数放在与包同名的 `article.go` 中。

模块的 `routes.go` 声明本模块的路径、HTTP 方法和访问策略，把 action 的 `HTTP` 方法注册为处理器，完整代码见[组织路由](routing.md#organizing-routes)。页面的处理器放在子包 `articleweb` 中，因为 templ 组件要导入 `article` 包的类型，放在同一个包里会造成循环导入。

<a id="http-actions"></a>
### 按业务动作组织代码

文件名和类型名表达业务动作，包名已经说明了业务对象。例如，“归档文章”使用 `archive.go` 和 `article.Archive`，无需再给文件名加 `action_` 前缀或给类型名加 `Action` 后缀。模块中没有一个包揽所有操作的 `Service`：动作多了就是文件多了，每个文件仍然只关心一件事。

动作很多、一个目录难以浏览时，可以按子领域拆分业务包，例如把评论拆到 `internal/comment`，而不是在包内再建子目录。

这些是推荐的组织与命名约定，不是框架限制。mygoravel 不会扫描目录，也不要求实现 `Action` 接口；简单的接口可以直接用函数作为处理器。

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
| action 文件 | 动作，snake_case | `create.go`、`publish_scheduled.go` |
| action 类型 | 动作，业务对象由包名表达 | `article.Create`、`article.Publish` |
| action 构造函数 | `New` + 类型名 | `NewCreate` |
| 业务方法 | `Handle` | `(*Publish).Handle` |
| 入口方法 | 入口名 | `(*Publish).HTTP`、`(*Publish).Job` |
| 输入、独有输出 | 动作名 + `Input` / `Output` | `CreateInput` |
| 资源路径 | 复数 | `/articles`、`/articles/{id}` |
| JSON 字段 | snake_case | `author_id` |
| OperationID | `资源-动作` | `article-create` |
| 业务错误码 | `资源.原因` | `article.not_found` |
| Go 缩写 | 全大写 | `ID`、`HTTP`、`DSN`、`TTL` |

<a id="framework-packages"></a>
## 框架包一览

mygoravel 的每个包都可以单独使用，按需引入即可。下面按文档目录的分类列出。

<a id="foundation"></a>
### 基础功能

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `config` | 读取 YAML 配置、展开环境变量、校验 | [配置](configuration.md) |
| `cleanup` | 按逆序关闭入口打开的资源 | [依赖装配](dependencies.md#closing-resources) |
| `errorx` | 定义带稳定错误码和分类的业务错误 | [错误处理](errors.md) |
| `logging` | 构造 slog 日志器，传递请求关联字段；`logging/logtest` 用于测试 | [日志](logging.md) |
| `identity` | 表示已认证的主体，并在 context 中传递 | [认证](authentication.md) |
| `page` | 固定分页与游标分页的参数和响应结构 | [分页](responses.md#pagination) |
| `httpx` | HTTP 中间件、平滑停机 | [中间件](middleware.md)、[请求生命周期](lifecycle.md) |
| `httpx/rest` | 基于 Huma 的路由注册、错误转换与 OpenAPI | [路由](routing.md) |
| `session` | 页面使用的 cookie 会话与 Bun 存储 | [会话](session.md) |
| `form` | 按 Huma 标签转换、验证文本形式的字段值，页面表单和表格导入共用 | [表单](forms.md#defining-forms) |
| `web` | 服务端渲染页面：渲染组件、会话登录、错误页、表单、提示消息、主题、SSE、带指纹的静态文件，内嵌 htmx 与 Alpine.js | [视图](views.md)、[表单](forms.md)、[前端资源](assets.md)、[服务端推送](sse.md) |
| `web/ui` | 内置 shadcn-templ 界面组件的样式表和脚本，按主题加载，提示、对话框与页面的衔接 | [界面组件](components.md) |
| `web/webtest` | 测试页面：保存会话 cookie，不经过登录页登录，读取 htmx 的响应头 | [页面测试](testing.md#page-tests) |

<a id="digging-deeper"></a>
### 深入了解

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `command` | 根命令、`Run`、`Bind`、`config` 命令、退出码 | [命令行](commands.md) |
| `command/migratecmd` | `migrate` 命令 | [migrate 命令](commands.md#migrate-command) |
| `command/httpcmd` | `openapi`、`routes` 命令 | [openapi 命令](commands.md#openapi-command) |
| `command/devcmd` | `dev` 命令：开发页面时监听文件变化、重启应用、刷新浏览器 | [dev 命令](commands.md#dev-command) |
| `lock` | 带过期时间的原子锁，数据库存储；`lock/redislock` 是 Redis 存储，`lock/locktest` 是测试用的内存存储与存储的契约测试 | [原子锁](locks.md)、[Redis 存储](locks.md#redis-store) |
| `ratelimit` | 按 key 限制操作频率，数据库与内存限流器，`Observe` 报告指标；`ratelimit/redislimit` 是 Redis 限流器 | [限流](rate-limiting.md)、[Redis 限流器](rate-limiting.md#redis-limiter) |
| `queue` | 持久化的异步任务队列，数据库驱动、重试、失败任务与指标 | [队列](queue.md) |
| `queue/redisqueue`、`queue/natsqueue` | Redis 与 NATS JetStream 队列驱动 | [Redis 驱动](queue.md#redis-driver)、[NATS 驱动](queue.md#nats-driver) |
| `schedule` | 按 cron 表达式定时执行任务，多个实例之间去重 | [定时任务](scheduling.md) |
| `broadcast` | 在实例之间广播消息，供 SSE、WebSocket 推送；`broadcast/redisbroadcast`、`broadcast/natsbroadcast` 是 Redis 与 NATS 后端，`broadcast/broadcasttest` 是测试用的内存后端与后端的契约测试 | [广播](broadcasting.md) |
| `realtime` | 嵌入 Centrifuge 的 WebSocket 实时频道：公开、私有与 presence 频道，频道就是广播的主题；`realtime/redispresence` 是在线成员的 Redis 存储，`realtime/realtimetest` 是存储的契约测试 | [实时频道](realtime.md) |
| `filesystem` | 基于单张 `files` 表管理上传、确认、公开地址和清理 | [文件存储与上传](filesystem.md) |
| `filesystem/local`、`filesystem/s3` | 本地与 S3 磁盘 | [文件存储与上传](filesystem.md#disks) |
| `mail` | 邮件消息、发送接口与模板渲染 | [邮件](mail.md) |
| `mail/smtp`、`mail/resend` | 通过 SMTP 服务器或 Resend API 发送 | [SMTP](mail.md#smtp)、[Resend](mail.md#resend) |
| `mail/logmail`、`mail/mailtest` | 开发时写进日志、测试时记录在内存中的发送器 | [邮件](mail.md#assembling-mailer)、[测试](mail.md#testing) |
| `outbound` | 只能访问公网地址的 HTTP 客户端，防止服务端请求伪造 | [出站请求](outbound.md) |
| `i18n` | 组合 go-i18n 消息目录 | [本地化](localization.md) |

<a id="application-features"></a>
### 应用功能

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `notification` | 站内通知收件箱，带已读状态 | [通知](notifications.md) |
| `activity` | 操作日志：在业务事务中记录谁对什么做了什么 | [操作日志](activity.md) |
| `sheet` | 读写 CSV 和 Excel（XLSX）表格，按结构体解码并验证导入的每一行 | [导入与导出](import-export.md) |
| `imports` | 在后台导入表格：进度、断点续跑、失败行下载 | [后台导入](import-export.md#background-imports) |
| `exports` | 在后台导出表格：进度，完成后上传到文件存储 | [后台导出](import-export.md#background-exports) |
| `webhook` | 向租户的端点推送事件，按 Standard Webhooks 签名与校验 | [Webhook](webhooks.md) |

<a id="ai-packages"></a>
### AI

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `ai` | 对话式 agent：后台运行、插话、工具审批、事件推送 | [AI 助手](ai.md) |
| `ai/postgres`、`ai/sqlite`、`ai/mysql` | 把对话保存在对应数据库中 | [AI 助手](ai.md#assembling) |
| `ai/openai`、`ai/openrouter` | 生成图片、合成与识别语音、计算向量和重排；`ai/openai` 还能调用决策模型 | [媒体](ai.md#media)、[决策](ai.md#decisions) |
| `ai/systemone` | 用 System One 格式调用决策模型 | [决策](ai.md#decisions) |
| `ai/aitest` | 按脚本回复的模型，测试 agent 时不访问真实服务 | [测试](ai.md#testing) |
| `document/split` | 把 Markdown 切分为可追溯到原文的分段，供检索和计算向量使用 | [文档转换与切分](documents.md#splitting) |
| `document/convert` | 把 PDF、Word、PowerPoint、Excel、CSV、HTML 等转换为 Markdown；各格式在子包中，`convert/all` 注册全部 | [文档转换与切分](documents.md#converting) |

<a id="security-packages"></a>
### 安全

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `apitoken` | 存在数据库中的 API 令牌，用作 JSON API 的 Bearer 凭据 | [API 令牌](api-tokens.md) |
| `authz` | 基于角色的权限：代码中声明的权限目录，角色按租户分配 | [授权](authorization.md)、[多租户](tenancy.md) |
| `hashing` | 密码的单向哈希与重新哈希 | [哈希](hashing.md) |
| `crypt` | 用应用密钥加密数据、签名和校验链接 | [加密与签名链接](encryption.md) |

<a id="database-packages"></a>
### 数据库

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `database` | 用应用选定的驱动打开连接池，`RunInTx` 与提交后回调，查询链路用的 `ChildSpans` | [数据库](database.md)、[数据库的链路](telemetry.md#database-tracing) |
| `database/postgres`、`database/sqlite`、`database/mysql` | 数据库驱动与约束错误判断，应用只 import 自己用的一个 | [数据库](database.md#sqlite) |
| `database/migrate` | 构造 goose 迁移器、新建迁移、发布功能包迁移、检查历史迁移 | [迁移](migrations.md) |
| `database/dbtest` | 为测试创建隔离的真实数据库 | [测试](testing.md#database-tests) |

缓存不在框架包中，你可以直接使用 [eko/gocache](https://github.com/eko/gocache)，详见[缓存](cache.md)；数据库查询直接使用 Bun 的原生 API，事务用 `database.RunInTx`，见[事务](database.md#transactions)。
