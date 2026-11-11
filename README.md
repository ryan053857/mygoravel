# mygoravel

mygoravel 是一个用于构建 HTTP API 和服务端渲染页面的 Go 应用框架。它为配置、路由、验证、错误处理、日志、认证、数据库、迁移和命令行提供一套统一的约定，同时让你直接使用社区组件的原生能力：chi 与 Huma、templ、htmx 与 shadcn-templ、slog 与 slog-multi / slog-formatter / slog-chi、Bun 与 pgx、goose、Cobra、go-i18n 和 cockroachdb/errors。

- **依赖一目了然**：没有服务容器和全局状态，所有依赖都通过普通的构造函数显式传递。
- **一个动作一个文件**：业务逻辑写成 action，HTTP 接口、队列任务、定时任务和命令行都是它的入口方法。
- **约定开箱即用**：请求 ID、统一的错误响应格式、强制的路由访问策略声明。
- **按需引入**：每个包都可以单独使用，最小的 API 不需要数据库。
- **接口文档自动生成**：OpenAPI 文档随代码生成，并可离线导出。

## 快速开始

```bash
go get github.com/ryan053857/mygoravel@main
```

需要 Go 1.27.3 或更高版本；数据库功能支持 PostgreSQL、SQLite 和 MySQL。从[安装](docs/installation.md)开始，几分钟即可运行第一个接口。

## 文档

完整的文档目录见 [docs/README.md](docs/README.md)。常用章节：

| 你想做的事 | 文档 |
| --- | --- |
| 运行第一个应用 | [安装](docs/installation.md) |
| 读取配置与密钥 | [配置](docs/configuration.md) |
| 组织项目代码 | [目录结构](docs/directory-structure.md) · [依赖装配](docs/dependencies.md) · [业务动作](docs/actions.md) |
| 编写接口 | [路由](docs/routing.md) · [请求](docs/requests.md) · [验证](docs/validation.md) · [响应](docs/responses.md) · [会话](docs/session.md) |
| 渲染页面 | [视图](docs/views.md) · [表单](docs/forms.md) · [界面组件](docs/components.md) · [服务端推送](docs/sse.md) · [广播](docs/broadcasting.md) · [实时频道](docs/realtime.md) |
| 查看全部路由 | [routes 命令](docs/commands.md#routes-command) |
| 处理错误与日志 | [错误处理](docs/errors.md) · [日志](docs/logging.md) · [指标与链路追踪](docs/telemetry.md) |
| 在后台执行任务 | [队列](docs/queue.md) · [定时任务](docs/scheduling.md) |
| 防止任务重复执行 | [原子锁](docs/locks.md) |
| 上传和管理文件 | [文件存储与上传](docs/filesystem.md) |
| 发送邮件 | [邮件](docs/mail.md) |
| 请求用户填写的地址 | [出站请求](docs/outbound.md) |
| 给用户发站内通知 | [通知](docs/notifications.md) |
| 记录谁改了什么 | [操作日志](docs/activity.md) |
| 导入导出 Excel 和 CSV | [导入与导出](docs/import-export.md) |
| 向租户推送事件 | [Webhook](docs/webhooks.md) |
| 构建 AI 助手 | [AI 助手](docs/ai.md) · [服务端推送](docs/sse.md#json-api) |
| 让 AI 引用 PDF、Word 等文档 | [文档转换与切分](docs/documents.md) |
| 保护接口 | [认证](docs/authentication.md) · [授权](docs/authorization.md) · [哈希](docs/hashing.md) · [加密与签名链接](docs/encryption.md) · [限流](docs/rate-limiting.md) |
| 使用数据库 | [数据库](docs/database.md) · [迁移](docs/migrations.md) |
| 测试与上线 | [测试](docs/testing.md) · [部署](docs/deployment.md) |

## 参与开发

欢迎参与 mygoravel 的开发！运行测试、CI 和文档写作约定请参阅[贡献指南](docs/contributions.md)。
