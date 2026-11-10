# mygoravel

mygoravel 是一个用于构建 HTTP API 的 Go 应用框架。它为配置、路由、验证、错误处理、日志、认证、数据库、迁移和命令行提供一套统一的约定，同时让你直接使用社区组件的原生能力：chi 与 Huma、slog 与 slog-multi / slog-formatter / slog-chi、Bun 与 pgx、goose、Cobra、go-i18n 和 cockroachdb/errors。

- **依赖一目了然**：没有服务容器和全局状态，所有依赖都通过普通的构造函数显式传递。
- **约定开箱即用**：请求 ID、统一的错误响应格式、强制的路由访问策略声明。
- **按需引入**：每个包都可以单独使用，最小的 API 不需要数据库。
- **接口文档自动生成**：OpenAPI 文档随代码生成，并可离线导出。

## 快速开始

```bash
go get github.com/ryan053857/mygoravel@main
```

需要 Go 1.27.3 或更高版本；数据库功能目前支持 PostgreSQL。从[安装](docs/installation.md)开始，几分钟即可运行第一个接口。

## 文档

完整的文档目录见 [docs/README.md](docs/README.md)。常用章节：

| 你想做的事 | 文档 |
| --- | --- |
| 运行第一个应用 | [安装](docs/installation.md) |
| 读取配置与密钥 | [配置](docs/configuration.md) |
| 组织项目代码 | [目录结构](docs/directory-structure.md) · [依赖装配](docs/dependencies.md) |
| 编写接口 | [路由](docs/routing.md) · [请求](docs/requests.md) · [验证](docs/validation.md) · [响应](docs/responses.md) |
| 查看全部路由 | [routes 命令](docs/commands.md#routes-command) |
| 处理错误与日志 | [错误处理](docs/errors.md) · [日志](docs/logging.md) |
| 保护接口 | [认证](docs/authentication.md) · [授权](docs/authorization.md) |
| 使用数据库 | [数据库](docs/database.md) · [迁移](docs/migrations.md) |
| 测试与上线 | [测试](docs/testing.md) · [部署](docs/deployment.md) |

## 参与开发

欢迎参与 mygoravel 的开发！运行测试、CI 和文档写作约定请参阅[贡献指南](docs/contributions.md)。
