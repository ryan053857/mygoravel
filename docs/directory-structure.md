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
    - [基础](#foundation)
    - [基础设施](#infrastructure)
    - [入口](#entry)
    - [功能](#features)

<a id="introduction"></a>
## 简介

Tenon 不要求固定的目录结构，也不会根据目录位置自动发现任何东西。一个小型应用完全可以只有一个 `main.go`，就像[安装](installation.md)中的示例那样。

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
│   ├── auth/                token 校验器；authhttp/ 是登录接口
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
│       └── articleweb/      读者看到的公开页面
│           ├── routes.go    页面路由
│           ├── handlers.go  页面处理器
│           └── views/       templ 组件及生成的 _templ.go
├── tenon.json               开发工具的配置，可以没有
├── package.json             前端的依赖和脚本：bun run dev 即 go tool tenon dev
├── vite.config.ts           root 指向 frontend
├── frontend/                编辑后台等单页应用的源码和嵌入构建产物的 frontend.go
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
| `internal/auth/authhttp/` | [API 令牌的登录与登出](api-tokens.md#login-and-logout)、[第三方登录](social-login.md) |
| `frontend/` | [单页应用](spa.md) |
| `migrations/` | [迁移](migrations.md#embedding-migrations) |

目录不必一次建齐，等真正有代码要放时再创建即可。

<a id="the-cmd-directory"></a>
### cmd 目录

`cmd` 目录存放应用的进程入口。每个子目录对应一个可执行文件，例如 `cmd/api` 构建出 API 服务。入口文件应当尽量短小，通常只有一行 `command.Execute(cli.New())`，见[命令行](commands.md#declaring-the-cli)。

<a id="the-bootstrap-directory"></a>
### internal/bootstrap 目录

`bootstrap` 包负责把应用“组装”起来：安装全局日志器、打开数据库连接、注册路由、启动队列和调度器。在 Tenon 中，这些工作都由普通的 Go 代码显式完成，[依赖装配](dependencies.md)一章会详细介绍。

<a id="business-packages"></a>
### 业务包

每个业务模块放在 `internal` 下的一个独立包中，例如 `internal/article`。业务包由 [action](actions.md) 组成：每个业务动作一个文件，包含它的依赖、业务规则，以及 HTTP 接口、队列任务等入口。多个 action 共用的模型、[业务错误](errors.md)和授权函数放在与包同名的 `article.go` 中。

模块的 `routes.go` 声明本模块的路径、HTTP 方法和访问策略，把 action 的 `HTTP` 方法注册为处理器，完整代码见[组织路由](routing.md#organizing-routes)。页面的处理器放在子包 `articleweb` 中，因为 templ 组件要导入 `article` 包的类型，放在同一个包里会造成循环导入。

<a id="http-actions"></a>
### 按业务动作组织代码

文件名和类型名表达业务动作，包名已经说明了业务对象。例如，“归档文章”使用 `archive.go` 和 `article.Archive`，无需再给文件名加 `action_` 前缀或给类型名加 `Action` 后缀。模块中没有一个包揽所有操作的 `Service`：动作多了就是文件多了，每个文件仍然只关心一件事。

动作很多、一个目录难以浏览时，可以按子领域拆分业务包，例如把评论拆到 `internal/comment`，而不是在包内再建子目录。

这些是推荐的组织与命名约定，不是框架限制。Tenon 不会扫描目录，也不要求实现 `Action` 接口；简单的接口可以直接用函数作为处理器。

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

Tenon 的每个包都可以单独使用，按需引入即可。顶层的包分为四层，包只引用同层或更低层的包：

```text
功能        apitoken  login  oauthserver  authz  notification  push  activity  feature  settings  operation  webhook  search  vector  sheet/imports  sheet/exports  ai
入口        httpx  web  realtime  mcpx  command
基础设施    database  queue  schedule  cachex  lock  ratelimit  captcha  broadcast  filesystem  imgproxy  mail  sms  pdf  outbound  sheet  document
基础        errorx  identity  config  cleanup  logging  telemetry  i18n  page  form  hashing  crypt  acquire
```

分层由[架构测试](contributions.md#architecture-tests)保证，新增的引用违反分层时测试会失败。下面由低到高列出每一层的包。

<a id="foundation"></a>
### 基础

错误、身份、配置、日志等最基本的类型。它们不依赖数据库、HTTP 和命令行组件，可以在任何地方使用。

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `acquire` | 在 context 中传递请求等待共享资源（例如数据库连接）的时限 | [连接用满时](database.md#pool-exhaustion) |
| `cleanup` | 按逆序关闭入口打开的资源 | [依赖装配](dependencies.md#closing-resources) |
| `config` | 读取 YAML 配置、展开环境变量、校验 | [配置](configuration.md) |
| `errorx` | 定义带稳定错误码和分类的业务错误 | [错误处理](errors.md) |
| `i18n` | 组合 go-i18n 消息目录 | [本地化](localization.md) |
| `hashing` | 密码的单向哈希与重新哈希 | [哈希](hashing.md) |
| `identity` | 表示已认证的主体，并在 context 中传递；API、页面和实时频道共用的主体确立规则 | [认证](authentication.md) |
| `logging` | 构造 slog 日志器，传递请求关联字段；`logging/logtest` 用于测试 | [日志](logging.md) |
| `telemetry` | 把日志、指标和链路用 OTLP 推送到一个接收端 | [可观测性](telemetry.md#setup) |
| `page` | 固定分页与游标分页的参数和响应结构 | [分页](responses.md#pagination) |
| `crypt` | 用应用密钥加密数据、签名和校验链接 | [加密与签名链接](encryption.md) |
| `form` | 按 Huma 标签转换、验证文本形式的字段值，页面表单和表格导入共用 | [表单](forms.md#defining-forms) |

<a id="infrastructure"></a>
### 基础设施

数据库、队列、锁、文件、邮件等与外部资源打交道的包。它们与请求从哪里进来无关：HTTP 处理器、队列任务和命令行都可以使用。

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `lock` | 带过期时间的原子锁；`lock/redislock` 是 Redis 存储，`lock/locktest` 是测试用的内存存储与存储的契约测试 | [原子锁](locks.md)、[Redis 存储](locks.md#redis-store) |
| `outbound` | 只能访问公网地址的 HTTP 客户端，防止服务端请求伪造；临时失败时重试可以安全重复的请求 | [出站请求](outbound.md) |
| `outbound/outboundtest` | 不连接网络的 HTTP 传输层：登记响应、记录请求，用于测试 | [测试](outbound.md#testing) |
| `database` | 打开 PostgreSQL 连接池，`RunInTx` 与提交后回调，约束错误判断，慢查询日志与语句的指标和链路 | [数据库](database.md)、[数据库的指标与链路](telemetry.md#database) |
| `database/migrate` | 构造 goose 迁移器、新建迁移、发布功能包迁移、检查历史迁移 | [迁移](migrations.md) |
| `database/dbtest` | 为测试创建隔离的真实数据库 | [测试](testing.md#database-tests) |
| `ratelimit` | 按 key 限制操作频率，内存限流器，`Observe` 报告指标；`ratelimit/redislimit` 是 Redis 限流器 | [限流](rate-limiting.md)、[Redis 限流器](rate-limiting.md#redis-limiter) |
| `captcha` | 在服务端校验 Turnstile、hCaptcha 的人机验证结果，挡住批量注册和提交表单的机器人 | [人机验证](login.md#captcha) |
| `document/split` | 把 Markdown 切分为可追溯到原文的分段，供检索和计算向量使用 | [文档转换与切分](documents.md#splitting) |
| `document/convert` | 把 PDF、Word、PowerPoint、Excel、CSV、HTML 等转换为 Markdown；各格式在子包中，`convert/all` 注册全部 | [文档转换与切分](documents.md#converting) |
| `pdf` | 把 templ 页面生成为 PDF 的接口 | [生成 PDF](pdf.md) |
| `pdf/gotenberg`、`pdf/pdftest` | 通过 Gotenberg 生成、测试时只记录 HTML 的生成器 | [启动 Gotenberg](pdf.md#running-gotenberg)、[测试](pdf.md#testing) |
| `mail` | 邮件消息、发送接口与模板渲染 | [邮件](mail.md) |
| `sms` | 短信：模板加变量的消息、发送接口和多个服务商的故障切换；`sms/aliyun`、`sms/tencent` 对接阿里云和腾讯云，`sms/logsms` 用于开发，`sms/smstest` 用于测试 | [短信](sms.md) |
| `mail/smtp`、`mail/resend`、`mail/ses` | 通过 SMTP 服务器、Resend API 或 Amazon SES 发送 | [SMTP](mail.md#smtp)、[Resend](mail.md#resend)、[SES](mail.md#ses) |
| `mail/logmail`、`mail/mailtest` | 开发时写进日志、测试时记录在内存中的发送器 | [邮件](mail.md#assembling-mailer)、[测试](mail.md#testing) |
| `queue` | 持久化的异步任务队列，数据库驱动、重试、失败任务与指标 | [队列](queue.md) |
| `queue/queuetest` | 测试中运行连接测试 nsqd 的 worker | [测试](queue.md#testing) |
| `schedule` | 按 cron 表达式定时执行任务，多个实例之间去重 | [定时任务](scheduling.md) |
| `cachex` | 在 Memcached 之上的 cache-aside 读取：合并并发加载、过期时间抖动、缓存空结果，缓存出错时直接加载 | [为文章详情加缓存](cache.md#caching-articles) |
| `broadcast` | 在实例之间广播消息，供 SSE、WebSocket 推送；`broadcast/redisbroadcast` 是 Redis 后端，`broadcast/broadcasttest` 是测试用的内存后端与后端的契约测试 | [广播](broadcasting.md) |
| `filesystem` | 基于单张 `files` 表管理上传、确认、公开地址和清理 | [文件存储与上传](filesystem.md) |
| `filesystem/local`、`filesystem/s3` | 本地与 S3 磁盘 | [文件存储与上传](filesystem.md#disks) |
| `imgproxy` | 生成签名的 imgproxy 地址，按需缩放图片，不保存缩略图 | [图片缩略图](filesystem.md#thumbnails) |
| `sheet` | 读写 CSV 和 Excel（XLSX）表格，按结构体解码并验证导入的每一行 | [导入与导出](import-export.md) |

<a id="entry"></a>
### 入口

请求进入应用的方式：JSON API、服务端渲染的公开页面、实时频道和命令行。

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `httpx` | HTTP 中间件（含 HTTP Basic 认证）、平滑停机、就绪检查、单页应用 | [中间件](middleware.md)、[请求生命周期](lifecycle.md)、[单页应用](spa.md) |
| `httpx/rest` | 基于 Huma 的路由注册、错误转换与 OpenAPI | [路由](routing.md) |
| `httpx/rest/clientgen` | 从路由生成 TypeScript 和 Go 客户端的 `client` 命令 | [TypeScript](clients.md#typescript)、[Go 客户端](clients.md#go) |
| `web` | 服务端渲染的公开页面：渲染组件、错误页、表单、主题、内容安全策略、带指纹的静态文件，内嵌 htmx 与 Alpine.js | [视图](views.md)、[表单](forms.md)、[前端资源](assets.md) |
| `web/ui` | 内置 shadcn-templ 界面组件的样式表和脚本，按主题加载；表单字段、分页、确认对话框与错误提示的衔接 | [界面组件](components.md) |
| `web/docs` | 把目录中的 Markdown 变成带侧边栏、本页目录和站内搜索的文档页，例如帮助中心 | [文档页](doc-pages.md) |
| `web/webtest` | 测试页面：保存 cookie，读取 htmx 的响应头 | [页面测试](testing.md#page-tests) |
| `realtime` | 嵌入 Centrifuge 的 WebSocket 实时频道：公开、私有与 presence 频道，频道就是广播的主题；`realtime/redispresence` 是在线成员的 Redis 存储，`realtime/realtimetest` 是测试用的内存存储与存储的契约测试 | [实时频道](realtime.md) |
| `mcpx` | 把 action 作为 MCP 工具提供给 AI 客户端：Bearer 认证、按权限筛选工具、按 Huma 规则校验参数；`mcpx/mcpxtest` 在测试中以指定的主体调用工具；`mcpx/mcpcmd` 是以 stdio 运行服务、列出工具的 `mcp` 命令 | [MCP 服务](mcp.md) |
| `command` | 根命令、`Run`、`Bind`、`config` 命令、退出码 | [命令行](commands.md) |
| `cmd/tenon` | 开发工具 `go tool tenon`：`dev`、`build`、`generate`，读取 `tenon.json` | [开发工具](tooling.md) |
| `command/httpcmd` | `openapi`、`routes` 命令 | [openapi 命令](commands.md#openapi-command) |
| `command/migratecmd` | `migrate` 命令 | [migrate 命令](commands.md#migrate-command) |
| `queue/queueweb` | 队列面板：复制槽和延迟任务的状态、失败任务的查看、重试和删除 | [队列面板](queue.md#panel) |
| `queue/queuecmd` | `queue failed` 命令：列出失败任务 | [队列状态](queue.md#stats-command) |
| `schedule/schedulecmd` | `schedules` 命令：列出定时任务、下一次执行的时间、是否正在执行和上一次的结果 | [列出任务的命令](scheduling.md#schedules-command) |

<a id="features"></a>
### 功能

建立在以上各层之上的应用功能。入口不依赖它们：路由、页面和命令由应用把功能接进来。

| 包 | 用途 | 相关文档 |
| --- | --- | --- |
| `apitoken` | 存在数据库中的 API 令牌，用作 JSON API 的 Bearer 凭据 | [API 令牌](api-tokens.md) |
| `login` | 登录标识、密码、验证码登录与注册、找回密码、二次验证 | [登录与注册](login.md) |
| `login/oauth` | Google、Apple、GitHub 等第三方账号登录（OpenID Connect、OAuth 2.0） | [第三方登录](social-login.md) |
| `login/passkey` | 用 passkey（WebAuthn）登录和注册 | [登录与注册](login.md#passkeys) |
| `oauthserver` | OAuth 2.1 授权服务器：页面中登录并同意后签发 OAuth 令牌，让 claude.ai、ChatGPT 的远程连接器访问 MCP 服务 | [OAuth 授权服务器](oauth-server.md) |
| `authz` | 基于角色的权限：代码中声明的权限目录，角色按租户分配 | [授权](authorization.md)、[多租户](tenancy.md) |
| `notification` | 站内通知收件箱，带已读状态 | [通知](notifications.md) |
| `push` | 移动推送：服务端登记设备，在业务事务中发送，按平台分批推送 | [移动推送](push.md) |
| `push/aliyun` | 对接阿里云移动推送，国内经厂商通道、海外经 FCM 和 APNs 送达 | [运行推送](push.md#delivering) |
| `push/pushtest` | 记录推送的 Provider，用于测试 | [测试](push.md#testing) |
| `feature` | 功能开关：代码中声明，按全员、租户、用户或比例开启 | [功能开关](features.md) |
| `settings` | 运行时可修改的设置：一组设置是一个结构体，保存为一行 JSON，默认值写在代码中，可按租户保存 | [设置](settings.md) |
| `activity` | 操作日志：在业务事务中记录谁对什么做了什么 | [操作日志](activity.md) |
| `operation` | 后台操作的状态与进度：任务中心、断点续跑、进度推送 | [后台操作](operations.md) |
| `webhook` | 向租户的端点推送事件，按 Standard Webhooks 签名与校验 | [Webhook](webhooks.md) |
| `search` | 全文搜索：在应用的数据库中建索引，事务中发起同步，过滤、排序、分面和高亮，不停服重建 | [全文搜索](search.md) |
| `search/gse` | 按词语切分中文的分词器，基于 gse 的词典 | [分词](search.md#tokenizers) |
| `search/searchcmd` | `search reindex` 命令：发起全文搜索索引或向量索引的重建，显示进度 | [重建索引](search.md#reindexing)、[重建向量索引](vector.md#reindexing) |
| `vector` | 向量检索：向量存放在 Qdrant 中，事务中发起同步，文本不变时沿用向量，按租户和字段过滤，语义与关键词混合检索，不停服重建和换模型 | [向量检索](vector.md) |
| `vector/vectortest` | 连接测试 Qdrant 的 Store，collection 测试结束时删除 | [测试](vector.md#testing) |
| `sheet/imports` | 在后台导入表格：进度、断点续跑、失败行下载 | [后台导入](import-export.md#background-imports) |
| `sheet/exports` | 在后台导出表格：进度，完成后上传到文件存储 | [后台导出](import-export.md#background-exports) |
| `ai` | 模型的配置和几种能力的接口：生成图片、合成与识别语音、计算向量、重排、决策 | [媒体](ai.md#media)、[决策](ai.md#decisions) |
| `ai/openai`、`ai/openrouter` | 生成图片、合成与识别语音、计算向量和重排；`ai/openai` 还能调用决策模型 | [媒体](ai.md#media)、[决策](ai.md#decisions) |
| `ai/systemone` | 用 System One 格式调用决策模型 | [决策](ai.md#decisions) |
| `ai/aitest` | 图片、语音、向量、重排和决策的替身 | [测试](ai.md#testing) |
| `ai/chat` | 对话式 agent：后台运行、插话、工具审批、事件推送 | [AI 助手](ai.md) |
| `ai/chat/chattest` | 按脚本回复的模型，测试 agent 时不访问真实服务 | [测试](ai.md#testing) |

缓存不在框架包中，你可以直接使用 [eko/gocache](https://github.com/eko/gocache)，详见[缓存](cache.md)；数据库查询直接使用 Bun 的原生 API，事务用 `database.RunInTx`，见[事务](database.md#transactions)。
