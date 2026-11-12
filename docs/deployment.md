# 部署

- [简介](#introduction)
- [构建应用](#building)
- [发布流程](#release-process)
    - [检查配置](#checking-configuration)
    - [执行迁移](#running-migrations)
    - [启动服务](#starting-the-server)
- [选择运行形态](#process-forms)
- [使用 systemd 运行](#systemd)
    - [发布新版本](#systemd-releases)
- [不停机升级](#zero-downtime-upgrades)
- [使用 Docker 运行](#docker)
- [健康检查](#health-checks)
- [平滑停止](#graceful-shutdown)
- [反向代理与网络](#reverse-proxies)
- [维护窗口](#maintenance-windows)
    - [在反向代理上返回 503](#maintenance-at-the-proxy)
    - [维护步骤](#maintenance-steps)
    - [不必整站停服时](#without-downtime)
- [日志](#logging)
- [备份与恢复](#backups)
    - [数据库](#database-backups)
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

如果入口位于 `cmd/api`，则把构建目标改为 `./cmd/api`。`-trimpath` 会去掉二进制中的本地路径信息。PostgreSQL 驱动是纯 Go 实现，可以用 `CGO_ENABLED=0` 构建不依赖系统库的静态二进制。

由于迁移文件、翻译文件和[静态文件](assets.md)都已通过 `embed` 嵌入，部署时只需要这一个二进制文件和 `config` 目录。构建前确认 templ 生成的代码和 Tailwind 生成的样式表（如果使用）都是最新的：二者都提交在版本库中，修改 `.templ` 后忘记重新生成，二进制里就是旧的页面。请将 `go.mod` 和 `go.sum` 提交到版本库，并记录每次构建对应的提交，以便出现问题时准确定位版本。

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

迁移器使用数据库锁，即使多个部署任务同时执行迁移，也只会串行运行。迁移成功后，再启动依赖新表结构的服务。滚动发布期间，新旧版本会同时运行，请确保数据库变更对两个版本都兼容，见[零停机变更](migrations.md#zero-downtime-changes)。

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

从一体改为分开时，只需要把部署中的 `start` 换成三组分别运行 `serve`、`work` 和 `schedule` 的实例。使用[实时频道](realtime.md)时，还可以再加一组 `realtime` 实例专门承接 WebSocket 连接，升级接口时不断开它们，见[单独部署实时连接](realtime.md#separate-process)。

两种形态都可以运行多个实例：

- **HTTP 服务**按常规做法放在负载均衡之后。
- **队列 worker** 竞争领取任务，每个任务只会被一个实例领取，增减实例不需要协调。
- **调度器**通过锁保证每次触发只执行一次，前提是所有实例共用同一个数据库或 Redis 锁存储，见[多个实例](scheduling.md#multiple-instances)。单独部署 `schedule` 时，运行两个实例可以避免单点。

worker 和调度器不监听端口，部署平台按进程是否存活判断它们的状态；它们出错退出时进程以非 0 状态结束，由平台重启。

所有部分共用同一个数据库连接池。估算 `max_open_conns` 时，要把 HTTP 请求、队列各 topic 的 `concurrency` 和同时执行的定时任务都算进去；分开部署时按各自的负载分别设置，所有实例的连接数之和不应超过数据库的上限。

<a id="systemd"></a>
## 使用 systemd 运行

在虚拟机或物理机上，用 systemd 管理应用进程：开机和崩溃后启动它，收集它的日志，停止时向它发送 `SIGTERM`。mygoravel 不提供生成或安装 unit 文件的命令，下面的模板 unit 改一下路径就能使用。

假设部署目录是 `/opt/blog`，其中有 `bin/api`、`config`，以及存放本地磁盘文件的 `storage`。密钥写在 `/etc/blog/env` 中，每行一个 `DATABASE_URL=...`，权限设为 `600`。应用以系统用户 `blog` 运行，只有 `storage` 可写：

```bash
sudo useradd --system --no-create-home --shell /usr/sbin/nologin blog
sudo install -d -o blog -g blog /opt/blog/storage
```

```ini
# /etc/systemd/system/blog@.service
[Unit]
Description=blog %i
After=network-online.target
Wants=network-online.target

[Service]
Type=notify-reload
NotifyAccess=all
RuntimeDirectory=blog/%i
PIDFile=/run/blog/%i/daemon.pid
User=blog
WorkingDirectory=/opt/blog
EnvironmentFile=/etc/blog/env
ExecStart=/opt/blog/bin/api %i --config=config/app.yaml,config/prod.yaml
Restart=on-failure
RestartSec=5s
RestartPreventExitStatus=2 3
TimeoutStopSec=45s
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/opt/blog/storage
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

文件名中的 `@` 表示这是一个模板，`%i` 是启用时写在 `@` 之后的实例名，在这里就是要运行的命令。所以同一个文件可以运行任何一种[运行形态](#process-forms)：

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now blog@start                          # 一体运行
sudo systemctl enable --now blog@serve blog@work blog@schedule  # 或者分开运行
```

从一体改为分开时，先 `sudo systemctl disable --now blog@start`，再启用另外三个。单独部署实时连接时再启用 `blog@realtime`，只改了接口的版本用 `sudo systemctl reload blog@serve` 升级，实时连接不受影响。

几处设置的用意：

- **就绪与升级。** `Type=notify-reload` 让 systemd 等应用报告就绪后才认为启动完成，`systemctl reload` 则触发[不停机升级](#zero-downtime-upgrades)。升级时报告就绪的是新进程，所以要用 `NotifyAccess=all`；新进程把自己的 PID 写进 `RuntimeDirectory` 中的 `daemon.pid`，旧进程退出后 systemd 从 `PIDFile` 找到它。这几项需要入口按[依赖装配](dependencies.md#assembling-the-application)一章使用 `command.Daemon`；不报告就绪的应用改用 `Type=exec`，并删掉 `NotifyAccess`、`RuntimeDirectory` 和 `PIDFile`。
- **重启。** 进程以非 0 状态退出时，`Restart=on-failure` 会重启它。数据库暂时连不上时，应用以 `ExitUnavailable`（4）退出，systemd 每 5 秒重试一次，直到连上为止。`RestartPreventExitStatus=2 3` 排除了用法错误和配置错误，这两种错误重启多少次都一样，unit 直接进入失败状态，`systemctl status` 能看到原因。
- **停止。** `TimeoutStopSec` 是发送 `SIGTERM` 后等待进程退出的时间，超时后发送 `SIGKILL`。它要大于三个 `shutdown_timeout` 中最大的一个，见[平滑停止](#graceful-shutdown)。上面的 45 秒是按队列和调度器默认的 30 秒设置的。
- **文件系统。** `ProtectSystem=strict` 把整个文件系统变为只读，只有 `ReadWritePaths` 中的目录可写，`PrivateTmp` 为进程提供一个独立的 `/tmp`。应用需要写入其他目录时，要把它加到 `ReadWritePaths` 中。

应用写到标准错误的 JSON 日志由 journald 收集：

```bash
journalctl -u 'blog@*' -f
```

<a id="systemd-releases"></a>
### 发布新版本

```bash
set -e
CONFIG=--config=config/app.yaml,config/prod.yaml
run() {
    sudo systemd-run --wait --pipe --quiet --uid=blog \
        -p EnvironmentFile=/etc/blog/env -p WorkingDirectory=/opt/blog "$@" $CONFIG
}

sudo install -m 755 api /opt/blog/bin/api.new
run /opt/blog/bin/api.new config check
sudo mv /opt/blog/bin/api.new /opt/blog/bin/api
run /opt/blog/bin/api migrate up

for unit in $(systemctl list-units 'blog@*' --state=active --plain --no-legend | awk '{print $1}'); do
    sudo systemctl reload "$unit"
    if [[ $(systemctl show -p StatusText --value "$unit") == "upgrade failed"* ]]; then
        systemctl status "$unit" --no-pager
        exit 1
    fi
done
```

- 新二进制先写到 `api.new`，用它检查配置，再改名覆盖。直接写入正在运行的二进制会因为 `Text file busy` 失败。
- `run` 用 `systemd-run` 执行一次性命令，它使用和服务相同的用户、环境变量和工作目录，以命令的退出码结束，失败时脚本就此停止。不要把迁移写进 unit 的 `ExecStartPre`：这样进程每次崩溃重启都会再执行一次迁移，分开运行时三个实例还会同时执行。
- `reload` 让每个 unit 不停机地换成新版本，见下一节。新版本起不来时旧版本继续运行，`reload` 仍然返回成功，所以脚本检查 `Status` 行：以 `upgrade failed` 开头就是升级失败，后面是原因。

<a id="zero-downtime-upgrades"></a>
## 不停机升级

`systemctl reload` 时，正在运行的进程用磁盘上的新二进制启动一个新进程，把监听端口交给它。新进程按[依赖装配](dependencies.md#assembling-the-application)的顺序打开数据库、装配全部运行部分，然后报告就绪；旧进程这时才开始平滑停止：排空请求，等待处理中的队列任务和定时任务。监听端口始终没有关闭，升级期间的请求不会失败。

新进程在报告就绪之前退出，或者一分钟内没有就绪，就会被结束，旧进程继续服务。配置写错、数据库连不上、路由冲突、定时任务的表达式写错，都属于这种情况。

这项功能由 `command.Daemon` 提供，只在设置了 `RuntimeDirectory=` 的 systemd `Type=notify-reload` 服务中启用，unit 文件见[上文](#systemd)。在开发环境、容器中运行时，应用的行为不变。上一次升级的旧进程还在排空时，`reload` 先等它退出，再开始这一次升级；systemd 等待 `reload` 的时间受 `TimeoutStartSec`（默认 90 秒）限制。

<a id="upgrade-overlap"></a>
### 新旧进程同时运行

新进程就绪之后，旧进程还要排空一段时间：请求最多 `http.shutdown_timeout`，队列任务最多 `queue.shutdown_timeout`，进行中的 [AI 回答](ai.md#deployment)可能更久。这段时间相当于部署了两个实例：

| 部分 | 升级时 | 需要 |
| --- | --- | --- |
| HTTP 服务 | 新连接由新进程处理，旧进程排空已有的请求 | 无 |
| 队列 worker | 新进程开始领取任务，旧进程停止领取并等待处理中的任务，超时的任务交给新进程重新执行 | 处理函数能安全地重复执行，见[队列](queue.md) |
| 调度器 | 新旧调度器短暂同时运行，锁保证每次触发只执行一次 | 无 |
| 服务端推送 | 浏览器重连到新进程，旧进程发布的事件经由后端送达 | 无 |
| AI 回答 | 旧进程等进行中的回答说完 | 配置 `chat.Cluster` 时浏览器重连后继续接收；否则回答说完后保存在对话中 |

同时运行期间数据库连接数是平时的两倍，`max_open_conns` 的两倍应当在数据库的连接上限之内。

<a id="upgrade-limits"></a>
### 限制

- `reload` 重新读取配置文件，但新进程继承旧进程的环境变量，修改 `/etc/blog/env` 或 unit 文件之后要执行 `restart`。
- 新进程在报告就绪之后崩溃，systemd 按 `Restart=` 用磁盘上的新二进制重启它，不会回到旧版本。
- 旧进程排空期间，`systemctl status` 显示的主进程仍是旧进程，它退出后才换成新进程。
- 机器重启、内核升级仍然会中断服务。需要避免这种中断时，部署多台机器放在负载均衡之后。

<a id="docker"></a>
## 使用 Docker 运行

应用是一个静态二进制，镜像只需要这个二进制和 `config` 目录：

```dockerfile
# Dockerfile
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/api ./cmd/api && mkdir /out/storage

FROM gcr.io/distroless/static-debian13:nonroot
WORKDIR /app
COPY --from=build /out/api /app/api
COPY config /app/config
COPY --from=build --chown=nonroot:nonroot /out/storage /app/storage
ENTRYPOINT ["/app/api", "--config=config/app.yaml,config/prod.yaml"]
CMD ["start"]
```

- **`ENTRYPOINT` 必须使用 exec 形式**，也就是 JSON 数组。这样应用就是容器中的 1 号进程，能直接收到 `docker stop` 发送的 `SIGTERM`。如果写成 `ENTRYPOINT ./api start` 这样的 shell 形式，信号会发给 shell，应用收不到，宽限期一到就被强制终止。
- **`--config` 写在 `ENTRYPOINT` 中**，`docker run blog migrate up` 这样的命令也会使用同样的配置。在命令后面再传 `--config`，会追加到这两个文件之后。
- **distroless 镜像带有 CA 证书和时区数据**，访问 HTTPS 接口、调度器使用 `In(loc)` 都不需要额外安装。
- **`storage` 用于本地磁盘，要挂载卷。** 镜像中预先建好这个目录并交给 `nonroot` 用户，否则新建的命名卷属于 root，应用无法写入。使用 S3 磁盘时，容器不需要任何卷。

用 Docker Compose 分开运行，并在启动前执行迁移：

```yaml
# compose.yaml
x-app: &app
  image: blog:${APP_VERSION}
  environment:
    APP_URL: https://blog.example.com
    DATABASE_URL: ${DATABASE_URL}
  restart: unless-stopped
  stop_grace_period: 45s
  depends_on:
    migrate:
      condition: service_completed_successfully

services:
  migrate:
    image: blog:${APP_VERSION}
    environment:
      DATABASE_URL: ${DATABASE_URL}
    command: [migrate, up]
  serve:
    <<: *app
    command: [serve]
    ports: ["8080:8080"]
  work:
    <<: *app
    command: [work]
  schedule:
    <<: *app
    command: [schedule]
```

`stop_grace_period` 不能省略：Docker 默认只等 10 秒就发送 `SIGKILL`，比队列和调度器默认的 `shutdown_timeout` 还短。一体运行时把三个服务换成一个运行 `start` 的服务即可；单独部署实时连接时，再加一个运行 `realtime` 的服务。

容器中不使用 `reload`：镜像不可变，升级就是换一个新容器。需要不中断服务时，让部署平台先启动新容器，等 `/readyz` 返回成功再停止旧容器，例如 [Kamal](https://kamal-deploy.org)、Docker Swarm 的 `update_config.order: start-first`、Kubernetes 的 `maxUnavailable: 0`。新旧容器同时运行时，同样要满足[上一节](#upgrade-overlap)的条件。

应用用到的其他服务也加进同一个 compose 文件，例如[生成 PDF](pdf.md#deployment) 用的 Gotenberg。

<a id="health-checks"></a>
## 健康检查

mygoravel 不会自动注册健康检查路由，你可以根据部署平台的要求自行注册，并在平台中配置相应的路径：

- **存活检查**（例如 `/livez`）：只确认进程可以响应；
- **就绪检查**（例如 `/readyz`）：确认服务没有在排空，并且必需的依赖可用。

具体写法见[请求生命周期](lifecycle.md#health-checks)。

<a id="graceful-shutdown"></a>
## 平滑停止

部署平台在停止实例时通常会发送 `SIGTERM`。请确保这个信号能传递到应用进程——例如在容器中直接以应用二进制作为入口进程，而不是通过一个不转发信号的 shell 脚本启动。

收到信号后，`httpx.Serve` 会开始排空请求，详见[请求生命周期](lifecycle.md#graceful-shutdown)。配置停机时间时请注意：

- `http.shutdown_timeout` 决定了排空请求的最长时间；`queue.shutdown_timeout` 决定了 worker 等待处理中任务的最长时间；`schedule.shutdown_timeout` 决定了调度器等待执行中任务的最长时间；
- 一体运行时三者同时开始停止，停机时间取其中最长的一个，而不是相加；
- 使用 [AI 助手](ai.md#deployment)时，HTTP 服务停止之后，清理资源时还要等进行中的回答说完，这段时间（文档示例中是 1 分钟）与上面的停机时间相加；
- 部署平台的停止宽限期（systemd 的 `TimeoutStopSec`、Docker 的 `stop_grace_period`、Kubernetes 的 `terminationGracePeriodSeconds`）应当大于上面的停机时间加上关闭数据库等资源所需的时间，否则进程可能在清理完成前被强制终止。超时未完成的队列任务会在其他实例上重新执行，定时任务不会补跑。

<a id="reverse-proxies"></a>
## 反向代理与网络

如果应用部署在负载均衡器或反向代理之后，请在 `http.client_ip.trusted_proxies` 中配置代理的实际网段，以便正确识别客户端地址，见[可信代理](middleware.md#trusted-proxies)。

反向代理转发请求时请保留原始的 `Host` 请求头（包括端口），例如 nginx 使用 `proxy_set_header Host $http_host;`。`$host` 会丢掉端口，[跨站请求伪造](session.md#csrf)防护会因为 `Origin` 与 `Host` 不一致而拒绝所有 `POST` 等请求。使用[服务端推送](sse.md#deployment)时，还要在反向代理上启用 HTTP/2。

TLS 终止和响应压缩通常在反向代理中配置；应用直接对外提供服务时，安装 [httpx.Compress](middleware.md#compression) 压缩响应。跨域请求使用内置的 [CORS](middleware.md#cors)。限流既可以在网关中配置，也可以使用内置的[限流](rate-limiting.md)；内存限流器按实例分别计数，多实例部署时请把这一点计入配置值，需要共享计数时使用数据库限流器。

<a id="maintenance-windows"></a>
## 维护窗口

按[发布流程](#release-process)发布不需要停服：迁移对新旧版本都兼容，`reload` 不停机地换上新版本。所以 mygoravel 不提供 Laravel 那样的 `down`、`up` 命令。少数情况下确实要停一段时间：

- 数据库本身要维护，例如升级主版本、迁移到新的实例；
- 一次变更无法拆成[对新旧版本都兼容](migrations.md#zero-downtime-changes)的几步；
- 出了故障，需要先挡住所有请求。

这时数据库往往不可用，应用读不到“是否在维护”，甚至根本没有运行。所以维护状态放在反向代理上：由它直接返回 `503` 并放行内部人员，应用停着也照样生效。

<a id="maintenance-at-the-proxy"></a>
### 在反向代理上返回 503

下面的 nginx 配置在每个请求上检查一个标记文件，文件存在就进入维护，办公网络的地址不受影响：

```nginx
geo $maintenance_bypass {
    default        0;
    203.0.113.0/24 1;  # 办公网络
}

server {
    # listen、ssl 等配置略

    set $maintenance 0;
    if (-f /etc/nginx/blog.maintenance) { set $maintenance 1; }
    if ($maintenance_bypass) { set $maintenance 0; }

    error_page 503 @maintenance;

    location / {
        if ($maintenance) { return 503; }
        proxy_pass http://blog;
        proxy_set_header Host $http_host;
    }

    location @maintenance {
        default_type application/problem+json;
        add_header Retry-After 1800 always;
        return 503 '{"title":"Service Unavailable","status":503,"code":"maintenance","detail":"系统维护中，请稍后再试"}';
    }
}
```

开始和结束维护就是创建和删除这个文件，不需要重新加载 nginx：

```bash
sudo touch /etc/nginx/blog.maintenance  # 开始维护
sudo rm /etc/nginx/blog.maintenance     # 结束维护
```

- **响应格式与应用的[错误响应](errors.md#what-clients-see)相同。** 前端按 `code` 为 `maintenance` 显示维护提示，按 `Retry-After` 的秒数稍后重试。运维后台的使用者在办公网络中，不会看到这个响应。
- **只拦截 nginx 自己返回的 `503`。** 没有开启 `proxy_intercept_errors` 时，应用返回的 `503`（例如停机时拒绝新的 [AI 回答](ai.md#deployment)）原样传给客户端。
- **放行按客户端地址判断。** nginx 前面还有负载均衡时，先用 [realip](https://nginx.org/en/docs/http/ngx_http_realip_module.html) 模块还原客户端地址，否则所有请求都来自负载均衡的地址。
- **有多台 nginx 时**，每一台都要创建标记文件。使用云负载均衡时，改用它自带的固定响应规则，例如 AWS ALB 的 `fixed-response` 动作。

<a id="maintenance-steps"></a>
### 维护步骤

以[使用 systemd 运行](#systemd)的部署为例，维护数据库并发布一个不兼容的版本：

```bash
sudo touch /etc/nginx/blog.maintenance
sudo systemctl stop 'blog@*'

# 维护数据库；换上新二进制并执行迁移，见“发布新版本”

sudo systemctl start blog@serve blog@work blog@schedule
curl -fsS https://blog.example.com/readyz  # 在办公网络中检查
sudo rm /etc/nginx/blog.maintenance
```

- **先挡住请求，再停止进程。** 顺序反过来时，进程停止后、标记生效前的请求会得到 `502`。
- **worker 和调度器也要停止。** 它们不经过反向代理，数据库维护期间会不断出错，不兼容的变更期间还可能用旧代码处理新数据。停止时 worker 等待处理中的任务完成，未执行的任务留在队列中，恢复后继续执行。
- **停止期间错过的定时任务不会补跑**，见[失败与停止](scheduling.md#failures-and-shutdown)。每天只执行一次的任务，恢复后需要[手动执行](scheduling.md#running-manually)一次。
- **检查通过后再结束维护。** 标记文件还在时，办公网络已经能访问新版本，确认无误再删除文件。

使用 Docker Compose 时，`docker compose stop` 和 `docker compose up -d` 分别对应上面的停止和启动。

<a id="without-downtime"></a>
### 不必整站停服时

很多时候只需要挡住一部分功能，其余的照常服务：

| 情况 | 做法 |
| --- | --- |
| 某个功能出了问题 | 把它的[功能开关](features.md#evaluation-order)对全员设为 `Off`，所有实例的下一次检查立即生效 |
| 维护期间只允许读 | 安装拒绝写请求的中间件，见[全局中间件](middleware.md#chi-middleware)中的示例 |
| 只暂停后台任务 | 分开运行时只停止 `work` 或 `schedule`，任务留在队列中，恢复后继续执行 |

<a id="logging"></a>
## 日志

`logging.New` 默认输出到标准错误，使用 JSON 格式。配置了 [`telemetry`](telemetry.md#setup) 时，日志还会和指标、链路一起推送到日志平台，标准错误上的日志照常输出。服务名和版本只在 `telemetry` 段配置，版本号默认取自构建信息，也可以在部署时通过环境变量指定：

```yaml
telemetry:
  endpoint: ${OTLP_ENDPOINT:-}
  service: blog
  version: ${APP_VERSION:-}
```

排查问题时，可以使用响应头 `X-Request-ID` 中的请求 ID，在日志中找到同一请求的访问日志和错误诊断。详见[日志](logging.md)。

<a id="backups"></a>
## 备份与恢复

应用的数据保存在两个地方：数据库，以及[文件存储](filesystem.md)使用的磁盘，两者都要备份。Redis 中的缓存、锁和限流计数丢失后会重新生成，不需要备份；使用 [Redis 队列驱动](queue.md#redis-driver)时，未执行的任务能否保住取决于 Redis 的持久化配置。

mygoravel 不提供备份命令。只有数据库自己的备份机制能持续备份并恢复到任意时间点；在应用中定时导出，数据量大时很慢，还会丢掉两次导出之间写入的数据。

<a id="database-backups"></a>
### 数据库

使用托管数据库（RDS、Cloud SQL 等）时，开启自动备份和时间点恢复（PITR）；自建时用 [pgBackRest](https://pgbackrest.org) 或 [WAL-G](https://github.com/wal-g/wal-g) 持续归档 WAL。

库很小时，用 `pg_dump` 定时导出也可以，但只能恢复到导出的那一刻。`pg_dump` 的主版本不能低于数据库服务器的主版本。

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
go test -race ./...
```

框架所需的 Go 版本以其 [go.mod](../go.mod) 为准，框架自身的数据库集成测试使用 PostgreSQL 18。你的应用额外引入的组件（例如缓存客户端）应当固定版本，并有各自的集成测试。

如果你在 CI 中启用了[已发布迁移检查](migrations.md#checking-in-ci)，它也会帮助你确认升级过程中没有意外修改已发布的迁移。
