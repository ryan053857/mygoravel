# 贡献指南

- [简介](#introduction)
- [开发环境](#development-environment)
- [运行测试](#running-tests)
    - [单元测试](#unit-tests)
    - [集成测试](#integration-tests)
    - [架构测试](#architecture-tests)
- [持续集成](#continuous-integration)
- [编码风格](#coding-style)
- [编写文档](#writing-documentation)

<a id="introduction"></a>
## 简介

本章面向 mygoravel 框架本身的开发者。如果你只是在自己的应用中使用 mygoravel，可以跳过这一章，应用的测试方法请参阅[测试](testing.md)。

<a id="development-environment"></a>
## 开发环境

- Go，版本以仓库 [go.mod](../go.mod) 中的声明为准；
- Docker，用于在本地启动集成测试所需的 PostgreSQL 18。

<a id="running-tests"></a>
## 运行测试

<a id="unit-tests"></a>
### 单元测试

在仓库根目录执行：

```bash
go test ./...
go test -race ./...
```

单元测试不依赖任何外部服务。

`filesystem/s3` 的存储流程测试使用 [gofakes3](https://github.com/johannesboyne/gofakes3) 的内存后端，通过 `httptest` 自动启动 S3 服务，真实 AWS SDK 会向它发送请求。它覆盖对象读写、服务端分片上传、预签名直传、分片合并和取消，不需要 AWS 凭证或独立 S3 容器。

`mail/smtp` 的测试在进程内启动一个最小的 SMTP 服务器，`mail/resend` 的测试用 `httptest` 代替 Resend API，都不需要外部服务或 API key。

<a id="integration-tests"></a>
### 集成测试

带有 `integration` 构建标签的测试需要一个真实的 PostgreSQL。先启动一个临时实例：

```bash
docker run --name mygoravel-test-postgres --rm -d \
  -e POSTGRES_PASSWORD=postgres \
  -p 127.0.0.1:55432:5432 postgres:18
```

确认数据库就绪：

```bash
docker exec mygoravel-test-postgres pg_isready -U postgres
```

然后在仓库根目录执行：

```bash
export DATABASE_TEST_DSN='postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable'
go test -race -count=1 -tags=integration ./...
```

没有设置 `DATABASE_TEST_DSN` 时，集成测试会失败而不是跳过。测试完成后执行 `docker stop mygoravel-test-postgres` 清理。

集成测试中包含对迁移失败场景的演练，例如非事务迁移执行到一半失败后，检查数据库状态、人工恢复并再次执行。

文件服务集成测试同时使用真实 PostgreSQL 和本地存储或 gofakes3，验证单张 `files` 表的状态变化、预签名直传、分片上传、确认、删除和清理。只运行文件相关测试可以执行 `go test -race -tags=integration ./filesystem/...`，仍需设置上述 `DATABASE_TEST_DSN`。

<a id="architecture-tests"></a>
### 架构测试

`internal/archtest` 检查框架各个包之间的依赖方向。例如 `errorx`、`identity` 等基础包不能依赖 HTTP 或数据库相关的包，`database` 只能依赖 `config`。如果你的修改引入了新的包依赖，请先确认它符合分层设计，再相应地更新测试中的规则。

<a id="continuous-integration"></a>
## 持续集成

CI 配置位于 [.github/workflows/ci.yml](../.github/workflows/ci.yml)，每次推送和 Pull Request 都会依次执行：

1. `gofmt` 格式检查；
2. `go vet ./...`；
3. `go test ./...`；
4. `go test -race ./...`；
5. `go test -race -tags=integration ./...`（使用 PostgreSQL 18 服务容器）。

提交之前，请在本地至少运行前四项。

<a id="coding-style"></a>
## 编码风格

- 代码使用 `gofmt` 格式化；
- 遵循[目录结构](directory-structure.md#naming-conventions)中的命名约定；
- 优先暴露社区组件的原生类型（`*bun.DB`、`*slog.Logger`、`*cobra.Command` 等），而不是再包装一层；
- 不引入全局状态，所有依赖都通过参数显式传递。

<a id="writing-documentation"></a>
## 编写文档

用户文档位于 `docs` 目录，组织结构和写作风格参考 [Laravel 文档](https://laravel.com/docs)：

- 每篇文档以 `# 标题` 开头，紧接着是本页的目录列表，第一节通常是“简介”；
- 每个 `##`、`###` 标题前放一个 `<a id="英文锚点"></a>`，目录和跨文档链接都使用这些锚点；
- 使用第二人称“你”，先说明“为什么”，再说明“怎么做”；
- 补充说明使用 `> [!NOTE]`，实用建议使用 `> [!TIP]`，需要特别注意的风险使用 `> [!WARNING]`；
- 代码示例要么是可以直接保存运行的完整文件，要么是片段——片段中出现的变量从哪里来，正文需要交代清楚；
- 新增文档后，记得在 [docs/README.md](README.md) 的目录中添加链接。
