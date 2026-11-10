# 部署

- [简介](#introduction)
- [构建应用](#building)
- [发布流程](#release-process)
    - [检查配置](#checking-configuration)
    - [执行迁移](#running-migrations)
    - [启动服务](#starting-the-server)
- [健康检查](#health-checks)
- [平滑停止](#graceful-shutdown)
- [反向代理与网络](#reverse-proxies)
- [日志](#logging)
- [使用 SQLite 部署](#sqlite)
    - [备份](#sqlite-backups)
- [升级依赖](#upgrading)

<a id="introduction"></a>
## 简介

当你准备把 mygoravel 应用部署到生产环境时，有几件重要的事情需要做好：构建一个可重复的二进制文件、在启动前检查配置和执行迁移、让部署平台正确地探测健康状态，以及在停止时平滑地排空请求。本章将逐一介绍。

<a id="building"></a>
## 构建应用

Go 应用会被编译成一个独立的二进制文件。对于根目录包含 `main.go` 的应用：

```bash
go build -trimpath -o bin/api .
```

如果入口位于 `cmd/api`，则把构建目标改为 `./cmd/api`。`-trimpath` 会去掉二进制中的本地路径信息。PostgreSQL 和 SQLite 的驱动都是纯 Go 实现，可以用 `CGO_ENABLED=0` 构建不依赖系统库的静态二进制。

由于迁移文件和翻译文件都已通过 `embed` 嵌入，部署时只需要这一个二进制文件和 `config` 目录。请将 `go.mod` 和 `go.sum` 提交到版本库，并记录每次构建对应的提交，以便出现问题时准确定位版本。

<a id="release-process"></a>
## 发布流程

一次典型的发布按以下顺序进行。下面的命令假设你已经按照[命令行](commands.md)一章装配了应用的命令行，并在部署目录（`config` 目录所在的目录）中执行。生产环境的配置由 `config/app.yaml` 和 `config/prod.yaml` 叠加而成（见[按环境覆盖配置](configuration.md#overriding-configuration)），为了简洁，先把它们记在一个变量里：

```bash
CONFIG=--config=config/app.yaml,config/prod.yaml
```

<a id="checking-configuration"></a>
### 检查配置

在发布前离线检查配置文件，可以尽早发现缺失的环境变量或写错的字段：

```bash
./bin/api config check $CONFIG
```

必要时，再用 `migrate status` 确认数据库可以连通，同时查看哪些迁移将要执行。数据库连不上时，命令以 `ExitUnavailable`（4）退出：

```bash
./bin/api migrate status $CONFIG
```

<a id="running-migrations"></a>
### 执行迁移

在启动新版本的服务**之前**执行数据库迁移：

```bash
./bin/api migrate up $CONFIG
```

在 PostgreSQL 上，迁移器使用数据库锁，即使多个部署任务同时执行迁移，也只会串行运行。迁移成功后，再启动依赖新表结构的服务。滚动发布期间，新旧版本会同时运行，请确保数据库变更对两个版本都兼容，见[零停机变更](migrations.md#zero-downtime-changes)。

<a id="starting-the-server"></a>
### 启动服务

```bash
./bin/api serve $CONFIG
```

数据库密码、token 等敏感信息建议通过部署平台的环境变量或密钥管理功能提供，在配置文件中用 `${DATABASE_URL}` 这样的方式引用，而不是直接写入文件。生产环境通常不放 `.env` 文件。

<a id="health-checks"></a>
## 健康检查

mygoravel 不会自动注册健康检查路由，你可以根据部署平台的要求自行注册，并在平台中配置相应的路径：

- **存活检查**（例如 `/livez`）：只确认进程可以响应；
- **就绪检查**（例如 `/readyz`）：确认服务没有在排空，并且必需的依赖可用。

具体写法见[请求生命周期](lifecycle.md#health-checks)。

<a id="graceful-shutdown"></a>
## 平滑停止

部署平台在停止实例时通常会发送 `SIGTERM`。请确保这个信号能传递到应用进程——例如在容器中直接以应用二进制作为入口进程，而不是通过一个不转发信号的 shell 脚本启动。

收到信号后，`httpx.ListenAndServe` 会开始排空请求，详见[请求生命周期](lifecycle.md#graceful-shutdown)。配置停机时间时请注意：

- `http.shutdown_timeout` 决定了排空请求的最长时间；
- 部署平台的停止宽限期（例如 Kubernetes 的 `terminationGracePeriodSeconds`）应当大于 `shutdown_timeout` 加上关闭数据库等资源所需的时间，否则进程可能在清理完成前被强制终止。

<a id="reverse-proxies"></a>
## 反向代理与网络

如果应用部署在负载均衡器或反向代理之后，请在 `http.client_ip.trusted_proxies` 中配置代理的实际网段，以便正确识别客户端地址，见[可信代理](middleware.md#trusted-proxies)。

TLS 终止、CORS 等功能，可以在反向代理中配置，也可以作为[自定义中间件](middleware.md#custom-middleware)加入应用。限流既可以在网关中配置，也可以使用内置的[限流](rate-limiting.md)；内存限流器按实例分别计数，多实例部署时请把这一点计入配置值，需要共享计数时使用数据库限流器。

<a id="logging"></a>
## 日志

`logging.New` 默认输出到标准错误，使用 JSON 格式。建议在配置中填写服务名，便于日志平台采集和检索；版本号默认取自构建信息，也可以在部署时通过环境变量指定：

```yaml
logs:
  service: blog
  version: ${APP_VERSION:-}
```

排查问题时，可以使用响应头 `X-Request-ID` 中的请求 ID，在日志中找到同一请求的访问日志和错误诊断。详见[日志](logging.md)。

<a id="sqlite"></a>
## 使用 SQLite 部署

使用 [SQLite](database.md#sqlite) 时，数据库就是服务器上的一个文件，部署方式与 PostgreSQL 有几处不同：

- **只运行一个实例。** 多台机器无法共享同一个数据库文件。同一台机器上的多个进程可以同时打开它，因此滚动发布时先启动新进程、再停止旧进程是安全的；在 Kubernetes 中请使用 `replicas: 1` 和 `Recreate` 策略，避免数据卷同时挂载到两个节点。
- **把数据库目录放在持久化存储上。** SQLite 在数据库文件旁边维护 `app.db-wal` 和 `app.db-shm` 两个文件，它们和 `app.db` 必须在同一目录，请挂载整个目录，而不是单个文件。不要使用 NFS 等网络文件系统，它们上面的文件锁不可靠。
- **迁移不加锁。** 请在同一个部署任务中先执行 `migrate up`，再启动服务，不要让多个任务同时执行迁移。

需要运行多个实例，或者写入量超过单机承受能力时，请改用 PostgreSQL。

<a id="sqlite-backups"></a>
### 备份

不要直接复制正在使用的数据库文件：此时部分数据可能还在 `-wal` 文件中，复制出来的文件可能不完整。请用 `VACUUM INTO` 生成一份一致的快照，它不会阻塞读写：

```bash
sqlite3 storage/app.db "VACUUM INTO '/backups/app-$(date +%F).db'"
```

应用的二进制中已经包含 SQLite，服务器上没有 `sqlite3` 命令时，可以把同一条语句写成应用的[自定义命令](commands.md#custom-commands)，用 `db.ExecContext(ctx, "VACUUM INTO ?", path)` 执行。需要持续备份时，可以使用 [Litestream](https://litestream.io) 把数据库实时复制到对象存储。

<a id="upgrading"></a>
## 升级依赖

升级 mygoravel 或其他依赖之后，请至少执行以下检查：

```bash
go test ./...
go test -race ./...
go test -race -tags=integration ./...
```

框架所需的 Go 版本以其 [go.mod](../go.mod) 为准，框架自身的数据库集成测试使用 PostgreSQL 18。你的应用额外引入的组件（例如缓存客户端）应当固定版本，并有各自的集成测试。

如果你在 CI 中启用了[已发布迁移检查](migrations.md#checking-in-ci)，它也会帮助你确认升级过程中没有意外修改已发布的迁移。
