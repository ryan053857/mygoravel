# 部署

- [简介](#introduction)
- [构建应用](#building)
- [发布流程](#release-process)
    - [检查配置](#checking-configuration)
    - [执行迁移](#running-migrations)
    - [启动服务](#starting-the-server)
- [选择运行形态](#process-forms)
- [健康检查](#health-checks)
- [平滑停止](#graceful-shutdown)
- [反向代理与网络](#reverse-proxies)
- [日志](#logging)
- [使用 SQLite 部署](#sqlite)
- [备份与恢复](#backups)
    - [数据库](#database-backups)
    - [SQLite](#sqlite-backups)
    - [文件](#file-backups)
    - [恢复之后](#after-restoring)
    - [恢复演练](#restore-drills)
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

如果入口位于 `cmd/api`，则把构建目标改为 `./cmd/api`。`-trimpath` 会去掉二进制中的本地路径信息。三种数据库的驱动都是纯 Go 实现，可以用 `CGO_ENABLED=0` 构建不依赖系统库的静态二进制。

由于迁移文件、翻译文件和[静态文件](assets.md)都已通过 `embed` 嵌入，部署时只需要这一个二进制文件和 `config` 目录。构建前确认 templ 生成的代码和 Tailwind 生成的样式表都是最新的：二者都提交在版本库中，修改 `.templ` 后忘记重新生成，二进制里就是旧的页面。请将 `go.mod` 和 `go.sum` 提交到版本库，并记录每次构建对应的提交，以便出现问题时准确定位版本。

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

在 PostgreSQL 和 MySQL 上，迁移器使用数据库锁，即使多个部署任务同时执行迁移，也只会串行运行。迁移成功后，再启动依赖新表结构的服务。滚动发布期间，新旧版本会同时运行，请确保数据库变更对两个版本都兼容，见[零停机变更](migrations.md#zero-downtime-changes)。

<a id="starting-the-server"></a>
### 启动服务

```bash
./bin/api start $CONFIG
```

`start` 在一个进程中运行 HTTP 服务、队列 worker 和定时任务调度器，也可以用 `serve`、`work`、`schedule` 分别启动它们，见下一节。

数据库密码、token 等敏感信息建议通过部署平台的环境变量或密钥管理功能提供，在配置文件中用 `${DATABASE_URL}` 这样的方式引用，而不是直接写入文件。生产环境通常不放 `.env` 文件。

<a id="process-forms"></a>
## 选择运行形态

按[命令行](commands.md#running-together)一章装配的应用有三个常驻部分，可以一起运行，也可以分开部署，代码相同，只是启动的命令不同：

| 形态 | 命令 | 适合 |
| --- | --- | --- |
| 一体 | `start` | 起步阶段和中小规模：一个进程、一份配置，部署和监控都最简单 |
| 分开 | `serve`、`work`、`schedule` | 任务量大，或者耗时的任务拖慢了接口；各自按负载增减实例 |

从一体改为分开时，只需要把部署中的 `start` 换成三组分别运行 `serve`、`work` 和 `schedule` 的实例。

两种形态都可以运行多个实例：

- **HTTP 服务**按常规做法放在负载均衡之后。
- **队列 worker** 竞争领取任务，每个任务只会被一个实例领取，增减实例不需要协调。
- **调度器**通过锁保证每次触发只执行一次，前提是所有实例共用同一个锁存储，即数据库或 Redis 存储，见[多个实例](scheduling.md#multiple-instances)。使用 `lock.NewMemoryStore()` 时，多个实例会重复执行每个任务。单独部署 `schedule` 时，运行两个实例可以避免单点。

worker 和调度器不监听端口，部署平台按进程是否存活判断它们的状态；它们出错退出时进程以非 0 状态结束，由平台重启。

所有部分共用同一个数据库连接池。估算 `max_open_conns` 时，要把 HTTP 请求、队列各 topic 的 `concurrency` 和同时执行的定时任务都算进去；分开部署时按各自的负载分别设置，所有实例的连接数之和不应超过数据库的上限。

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

- `http.shutdown_timeout` 决定了排空请求的最长时间；`queue.shutdown_timeout` 决定了 worker 等待处理中任务的最长时间；`schedule.shutdown_timeout` 决定了调度器等待执行中任务的最长时间；
- 一体运行时三者同时开始停止，停机时间取其中最长的一个，而不是相加；
- 使用 [AI 助手](ai.md#deployment)时，HTTP 服务停止之后，清理资源时还要等进行中的回答说完，这段时间（文档示例中是 1 分钟）与上面的停机时间相加；
- 部署平台的停止宽限期（例如 Kubernetes 的 `terminationGracePeriodSeconds`）应当大于上面的停机时间加上关闭数据库等资源所需的时间，否则进程可能在清理完成前被强制终止。超时未完成的队列任务会在其他实例上重新执行，定时任务不会补跑。

<a id="reverse-proxies"></a>
## 反向代理与网络

如果应用部署在负载均衡器或反向代理之后，请在 `http.client_ip.trusted_proxies` 中配置代理的实际网段，以便正确识别客户端地址，见[可信代理](middleware.md#trusted-proxies)。

反向代理转发请求时请保留原始的 `Host` 请求头（包括端口），例如 nginx 使用 `proxy_set_header Host $http_host;`。`$host` 会丢掉端口，[跨站请求伪造](session.md#csrf)防护会因为 `Origin` 与 `Host` 不一致而拒绝所有 `POST` 等请求。使用[服务端推送](sse.md#deployment)时，还要在反向代理上启用 HTTP/2。

TLS 终止通常在反向代理中配置；跨域请求使用内置的 [CORS](middleware.md#cors)。限流既可以在网关中配置，也可以使用内置的[限流](rate-limiting.md)；内存限流器按实例分别计数，多实例部署时请把这一点计入配置值，需要共享计数时使用数据库限流器。

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
- **队列和定时任务都可以使用 SQLite。** 队列的领取与业务写入共用唯一的写者，长时间不提交的写事务会让 worker 等待，见[队列](queue.md#preparing-tables)。

需要运行多个实例，或者写入量超过单机承受能力时，请改用 PostgreSQL。备份方式见 [SQLite 的备份](#sqlite-backups)。

<a id="backups"></a>
## 备份与恢复

应用的数据保存在两个地方：数据库，以及[文件存储](filesystem.md)使用的磁盘，两者都要备份。Redis 中的缓存、锁和限流计数丢失后会重新生成，不需要备份；使用 [Redis 队列驱动](queue.md#redis-driver)时，未执行的任务能否保住取决于 Redis 的持久化配置。

mygoravel 不提供备份命令。只有数据库自己的备份机制能持续备份并恢复到任意时间点；在应用中定时导出，数据量大时很慢，还会丢掉两次导出之间写入的数据。

<a id="database-backups"></a>
### 数据库

| 数据库 | 建议 |
| --- | --- |
| PostgreSQL | 使用托管数据库（RDS、Cloud SQL 等）时，开启自动备份和时间点恢复（PITR）；自建时用 [pgBackRest](https://pgbackrest.org) 或 [WAL-G](https://github.com/wal-g/wal-g) 持续归档 WAL |
| MySQL | 托管数据库同上；自建时用 [Percona XtraBackup](https://docs.percona.com/percona-xtrabackup/) 做全量备份，加上 binlog 恢复到任意时间点 |
| SQLite | 见[下文](#sqlite-backups) |

库很小时，用 `pg_dump` 或 `mysqldump --single-transaction` 定时导出也可以，但只能恢复到导出的那一刻。`pg_dump` 的主版本不能低于数据库服务器的主版本。

<a id="sqlite-backups"></a>
### SQLite

不要直接复制正在使用的数据库文件：此时部分数据可能还在 `-wal` 文件中，复制出来的文件可能不完整。请用 `VACUUM INTO` 生成一份一致的快照，它不会阻塞读写：

```bash
sqlite3 storage/app.db "VACUUM INTO '/backups/app-$(date +%F).db'"
```

应用的二进制中已经包含 SQLite，服务器上没有 `sqlite3` 命令时，可以把同一条语句写成应用的[自定义命令](commands.md#custom-commands)，用 `db.ExecContext(ctx, "VACUUM INTO ?", path)` 执行。需要持续备份时，可以使用 [Litestream](https://litestream.io) 把数据库实时复制到对象存储。

<a id="file-backups"></a>
### 文件

文件存储用 `files` 表记录文件，文件内容保存在磁盘上。`Delete` 之后，[清理任务](filesystem.md#cleanup)几分钟内就会删除磁盘上的对象。把数据库恢复到较早的时间点后，这段时间里删除的文件又会出现在 `files` 表中，磁盘上的对象却已经没有了。

- **S3 磁盘**：为桶开启[版本控制](https://docs.aws.amazon.com/AmazonS3/latest/userguide/Versioning.html)。对象被删除时只是加上一个删除标记，删掉这个标记就能找回对象。再配置生命周期规则，让旧版本在一段时间后过期，这段时间不短于数据库备份的保留期。MinIO 等 S3 兼容存储同样支持版本控制。
- **本地磁盘**：用 [restic](https://restic.net) 等工具定时备份 `disks.local.root` 目录，并保留历史快照。先备份数据库，再备份文件，两次备份的间隔尽量短。恢复后如果发现有记录找不到对应的对象，可以到更早的快照中找回。

<a id="after-restoring"></a>
### 恢复之后

数据库回到了过去，但已经发出去的邮件、已经调用过的外部接口不会跟着撤回。恢复后要注意：

- **任务会再执行一次。** 做备份时还在 `queue_jobs` 中的任务，恢复后会再执行一遍，其中有些在恢复前已经执行过：邮件可能再发一封，webhook 会用相同的 `webhook-id` 再投递一次。处理函数本来就需要能安全地重复执行，见[队列](queue.md)。
- **撤销过的凭据又能用了。** 备份之后撤销的 [API token](api-tokens.md#revoking-tokens) 和[会话](session.md)，恢复后会重新生效。如果这段时间里有用户被封禁或改过密码，恢复后对这些用户再调用一次 `RevokeSubject`。无法逐个确认时，清空 `sessions` 表并删除 `api_tokens` 中 `kind = 'login'` 的行，让所有人重新登录；这段时间里撤销过的个人访问令牌需要再撤销一次。

<a id="restore-drills"></a>
### 恢复演练

没有恢复过的备份并不可靠。请定期把备份恢复到一个临时数据库，再用同一个二进制连上去检查：

```bash
./bin/api migrate status --config=config/app.yaml,config/restore.yaml
```

`config/restore.yaml` 只把数据库地址改为临时库。确认迁移版本与预期一致后，再抽查几张业务表中最新的数据。每次演练都记下恢复用了多长时间，出事时停服大约也要这么久。

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
