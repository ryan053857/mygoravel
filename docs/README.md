# mygoravel 文档

欢迎使用 mygoravel！mygoravel 是一个用于构建 HTTP API 和服务端渲染页面的 Go 应用框架。它为配置、路由、验证、错误处理、日志、认证、数据库和迁移提供一套统一的约定，同时让你直接使用 chi、Huma、slog、Bun、goose、Cobra 等社区组件的原生能力。

如果你是第一次接触 mygoravel，建议从[安装](installation.md)开始，用几分钟跑通第一个接口，然后按照下面的目录顺序阅读。每篇文档都可以独立查阅，遇到需要其他章节知识的地方，正文会给出链接。

## 序章

- [贡献指南](contributions.md)

## 入门

- [安装](installation.md)
- [配置](configuration.md)
- [目录结构](directory-structure.md)
- [部署](deployment.md)

## 架构概念

- [请求生命周期](lifecycle.md)
- [依赖装配](dependencies.md)
- [业务动作](actions.md)

## 基础功能

- [路由](routing.md)
- [中间件](middleware.md)
- [请求](requests.md)
- [验证](validation.md)
- [响应](responses.md)
- [客户端代码](clients.md)
- [视图](views.md)
- [表单](forms.md)
- [前端资源](assets.md)
- [界面组件](components.md)
- [会话](session.md)
- [错误处理](errors.md)
- [日志](logging.md)
- [可观测性](telemetry.md)

## 深入了解

应用常用的基础设施，其他功能也建立在它们之上。

- [命令行](commands.md)
- [缓存](cache.md)
- [原子锁](locks.md)
- [限流](rate-limiting.md)
- [队列](queue.md)
- [定时任务](scheduling.md)
- [服务端推送](sse.md)
- [广播](broadcasting.md)
- [实时频道](realtime.md)
- [文件存储与上传](filesystem.md)
- [邮件](mail.md)
- [生成 PDF](pdf.md)
- [出站请求](outbound.md)
- [本地化](localization.md)

## 应用功能

现成的业务功能，各自带有数据表，按需引入。

- [通知](notifications.md)
- [移动推送](push.md)
- [操作日志](activity.md)
- [功能开关](features.md)
- [后台操作](operations.md)
- [导入与导出](import-export.md)
- [Webhook](webhooks.md)
- [全文搜索](search.md)

## AI

- [AI 助手](ai.md)
- [文档转换与切分](documents.md)

## 安全

- [认证](authentication.md)
- [登录与注册](login.md)
- [API 令牌](api-tokens.md)
- [第三方登录](social-login.md)
- [完整示例：编辑后台与读者 App](account-examples.md)
- [多租户](tenancy.md)
- [授权](authorization.md)
- [哈希](hashing.md)
- [加密与签名链接](encryption.md)

## 数据库

- [快速入门](database.md)
- [迁移](migrations.md)
- [Redis](redis.md)

## 测试

- [快速入门](testing.md)

---

<a id="reading-examples"></a>

### 关于代码示例

文档中的例子来自同一个博客应用：作者可以撰写、修改和发布文章，草稿只有作者本人能看到。它的完整结构见[目录结构](directory-structure.md#application-layout)，每个文件的完整代码分布在相应的章节中。

文档中的代码分为两类：

- **完整文件**：正文会明确说明“下面是完整的 `xxx.go`”，你可以直接保存到自己的 Go module 中运行。
- **代码片段**：展示需要加入到已有 action、处理器或入口函数中的部分。片段中用到的变量（例如 `db`、`logger`、`api`）从哪里来，正文都会交代。

文档与同一版本的代码对应。框架依赖的精确版本以仓库中的 [go.mod](../go.mod) 为准。
