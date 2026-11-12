# 测试：快速入门

- [简介](#introduction)
- [运行测试](#running-tests)
- [HTTP 测试](#http-tests)
    - [测试认证与错误响应](#testing-auth-and-errors)
- [页面测试](#page-tests)
- [测试 action](#testing-actions)
    - [测试命令入口](#testing-action-commands)
- [准备测试数据](#test-data)
- [控制时间](#controlling-time)
- [测试配置](#testing-configuration)
- [数据库测试](#database-tests)
    - [创建测试数据库](#creating-test-databases)
    - [在本地运行数据库测试](#running-database-tests-locally)
- [命令测试](#command-tests)
- [测试平滑停止](#testing-shutdown)
- [测试 OpenAPI 文档](#testing-openapi)

<a id="introduction"></a>
## 简介

mygoravel 在设计时就考虑了测试。由于所有依赖都是显式传入的，你可以在测试中轻松构造 API、action 和命令，而不需要启动整个应用。

mygoravel 不引入额外的测试框架，直接使用 Go 标准库的 `testing`、`net/http/httptest` 和 `go test` 命令。需要断言日志时，使用 `logging/logtest` 捕获，见[测试日志](logging.md#testing-logs)。代码请求外部 HTTP 服务时，用 `outbound/outboundtest` 登记响应并检查发出的请求，见[出站请求](outbound.md#testing)。测试登录流程时，用 `login/logintest` 读取发出的验证码、计算二次验证的数字，见[登录与注册](login.md#testing)。

<a id="running-tests"></a>
## 运行测试

在应用根目录中执行：

```bash
go test ./...
```

建议同时开启竞态检测，它可以帮你发现并发访问共享数据的问题：

```bash
go test -race ./...
```

需要真实数据库的测试见[数据库测试](#database-tests)。

<a id="http-tests"></a>
## HTTP 测试

测试 HTTP 接口时，不需要真正监听端口。用 `httptest` 创建请求和响应记录器，然后直接调用 `api.Handler()` 即可。

请调用应用自己的路由注册函数，而不是在测试中重新声明路由，这样测试覆盖的就是真实的路由配置、认证和输入验证。测试文件按 action 命名。下面是博客应用完整的 `internal/article/create_test.go`，它测试[创建文章](routing.md#http-actions)的 HTTP 入口：

```go
package article_test

import (
    "encoding/json"
    "net/http"
    "net/http/httptest"
    "strings"
    "testing"

    "example.com/blog/internal/article"
    "example.com/blog/internal/auth"
    "example.com/blog/internal/user"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
)

func newTestAPI() *rest.API {
    api := rest.New(rest.Options{Accounts: identity.Accounts{user.Type: nil}})
    validate := auth.Validator(auth.Config{Credentials: []auth.Credential{
        {Token: "alice-token", SubjectID: "alice", Permissions: []string{"articles"}},
        {Token: "guest-token", SubjectID: "guest"},
    }})
    // 这些请求在调用 Handle 之前就会被拒绝，所以不需要数据库
    article.Register(api, rest.RequireBearer(api, validate, "articles"), nil)
    return api
}

func TestCreateRejectsBadRequests(t *testing.T) {
    const valid = `{"title":"Hello World","slug":"hello-world","content":"……"}`
    tests := []struct {
        name     string
        token    string
        body     string
        status   int
        code     string
        location string
    }{
        {"没有 token", "", valid, http.StatusUnauthorized, "auth.unauthenticated", ""},
        {"缺少权限", "guest-token", valid, http.StatusForbidden, "auth.forbidden", ""},
        {"slug 格式不对", "alice-token", `{"title":"Hello World","slug":"Hello World","content":""}`, http.StatusUnprocessableEntity, "", "body.slug"},
    }
    api := newTestAPI()
    for _, tc := range tests {
        t.Run(tc.name, func(t *testing.T) {
            req := httptest.NewRequest(http.MethodPost, "/articles", strings.NewReader(tc.body))
            req.Header.Set("Content-Type", "application/json")
            if tc.token != "" {
                req.Header.Set("Authorization", "Bearer "+tc.token)
            }
            rr := httptest.NewRecorder()
            api.Handler().ServeHTTP(rr, req)

            var problem struct {
                Code   string `json:"code"`
                Errors []struct {
                    Location string `json:"location"`
                } `json:"errors"`
            }
            if err := json.Unmarshal(rr.Body.Bytes(), &problem); err != nil {
                t.Fatal(err)
            }
            if rr.Code != tc.status || problem.Code != tc.code {
                t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
            }
            if tc.location != "" && (len(problem.Errors) == 0 || problem.Errors[0].Location != tc.location) {
                t.Fatalf("errors=%s", rr.Body.String())
            }
        })
    }
}
```

这个测试用表格列出了三种在调用 `Handle` 之前就会被拒绝的请求：没有 token、缺少权限、输入格式不对。它们不会触及数据库，所以 `db` 可以传 `nil`，测试运行得很快。

<a id="testing-auth-and-errors"></a>
### 测试认证与错误响应

请通过完整的 `api.Handler()` 来测试认证、验证错误和越权访问，而不是直接调用处理器函数。这样测试才能覆盖认证中间件、输入验证和错误转换的完整过程。一组典型的接口测试包括：

- 不带 `Authorization` 头的请求返回 `401`；
- 缺少权限的 token 返回 `403`；
- 不合法的请求体返回 `422`，并检查 `errors[].location`；
- 访问他人的资源返回 `403` 或 `404`；
- 错误响应中的 `code` 与预期的错误码一致。

前三种由上面的测试覆盖。后两种取决于数据库中的数据，更适合直接测试 action，见下一节。

<a id="page-tests"></a>
## 页面测试

[服务端渲染的页面](views.md)用 `web/webtest` 测试。`webtest.Client` 像浏览器一样向页面路由发送请求，保存会话 cookie，`Login` 不经过登录页就能以某个主体登录。下面是 `internal/article/articleweb/update_test.go`，它测试[表单](forms.md#handling-submissions)一章的修改文章：

```go
package articleweb_test

import (
    "context"
    "net/http"
    "net/url"
    "strings"
    "testing"

    "github.com/alexedwards/scs/v2/memstore"
    "github.com/go-chi/chi/v5"

    "example.com/blog/internal/article"
    "example.com/blog/internal/article/articleweb"
    "example.com/blog/internal/user"
    "example.com/blog/migrations"
    "github.com/ryan053857/mygoravel/database/dbtest"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/session"
    "github.com/ryan053857/mygoravel/web"
    "github.com/ryan053857/mygoravel/web/ui"
    "github.com/ryan053857/mygoravel/web/webtest"
)

func TestUpdate(t *testing.T) {
    db := dbtest.Open(t, migrations.FS)
    alice := identity.Subject{Type: user.Type, ID: "alice", Permissions: []string{"articles"}}
    item, err := article.NewCreate(db).Handle(context.Background(), alice, article.Changes{Title: "Hello", Slug: "hello"})
    if err != nil {
        t.Fatal(err)
    }

    var cfg session.Config
    cfg.SetDefaults()
    pages := web.New(web.Options{
        Sessions:  session.New(cfg, memstore.New()),
        Login:     "/login",
        Accounts:  identity.Accounts{user.Type: nil},
        ErrorPage: ui.ErrorPage,
    })
    router := chi.NewRouter()
    articleweb.Register(router, pages, db)
    c := webtest.New(t, router)
    path := "/admin/articles/" + item.ID.String()

    // 未登录时跳到登录页
    if res := c.Put(path, url.Values{"title": {"Hi"}}, webtest.HTMX); !strings.HasPrefix(res.Redirect(), "/login?next=") {
        t.Fatalf("anonymous: %d %q", res.Code, res.Redirect())
    }

    c.Login(pages, alice)
    // 验证失败：422，表单回显错误
    res := c.Put(path, url.Values{"title": {""}, "slug": {"hello"}}, webtest.HTMX)
    if res.Code != http.StatusUnprocessableEntity || !strings.Contains(res.Text(), `aria-invalid="true"`) {
        t.Fatalf("invalid: %d %s", res.Code, res.Text())
    }
    // 保存后跳到列表页，提示由列表页显示
    res = c.Put(path, url.Values{"title": {"Hello World"}, "slug": {"hello"}}, webtest.HTMX)
    if res.Redirect() != "/admin/articles" {
        t.Fatalf("saved: %d %q", res.Code, res.Redirect())
    }
    if page := c.Get(res.Redirect()); !strings.Contains(page.Text(), "文章已保存") {
        t.Fatal("flash not shown")
    }
}
```

会话存储用 scs 的 `memstore`，页面路由与应用相同，测试覆盖了登录检查、表单验证和跳转。`Login` 的主体与 `pages.Login` 保存的相同；一个应用有[多组页面](views.md#multiple-areas)时，对每组分别 `Login`。

请求默认是浏览器的整页请求。`webtest.HTMX` 让它像 htmx 替换页面中某个元素时发出的一样，`webtest.Header` 设置其他请求头，例如 `Accept-Language`。multipart 表单等其他请求用 `c.Do` 发送。响应除了 `Code`、`Text()`，还可以读取 htmx 的响应头：

| 方法 | 返回 |
| --- | --- |
| `Redirect()` | 跳转的目标：`303` 的 `Location`，或 htmx 请求的 `HX-Redirect` |
| `Flashes()` | 随 htmx 响应送达的[提示](views.md#flash-messages)，包括局部请求的错误 |
| `Event(name)` | `web.Trigger` 触发的事件的 detail，例如 `res.Event(web.DialogCloseEvent)` 的 `id` 是关闭的对话框 |
| `PushURL()` | `web.PushURL` 推送的地址 |

`webtest` 不运行浏览器中的脚本：对话框能否打开、提示是否弹出这类交互，由框架的组件保证，应用测试服务端的响应即可。

<a id="testing-actions"></a>
## 测试 action

业务规则（尤其是资源所有权检查）应当通过直接调用 action 的 `Handle` 来测试。由于主体是作为参数传入的，你可以很方便地模拟不同的调用者。下面是 `internal/article/article_test.go`，它在一个真实的测试数据库上验证草稿的可见性和发布规则：

```go
package article_test

import (
    "context"
    "errors"
    "testing"

    "example.com/blog/internal/article"
    "example.com/blog/internal/user"
    "example.com/blog/migrations"
    "github.com/ryan053857/mygoravel/database/dbtest"
    "github.com/ryan053857/mygoravel/identity"
)

func TestDraftVisibilityAndPublishing(t *testing.T) {
    ctx := context.Background()
    db := dbtest.Open(t, migrations.FS)
    create, get, publish := article.NewCreate(db), article.NewGet(db), article.NewPublish(db)
    alice := identity.Subject{Type: user.Type, ID: "alice"}
    bob := identity.Subject{Type: user.Type, ID: "bob"}

    item, err := create.Handle(ctx, alice, article.Changes{Title: "Hello World", Slug: "hello-world"})
    if err != nil {
        t.Fatal(err)
    }

    // 草稿只有作者能看到
    if _, err := get.Handle(ctx, bob, item.ID); !errors.Is(err, article.ErrNotFound) {
        t.Fatalf("bob read a draft: %v", err)
    }
    // 只有作者能发布，而且不能重复发布
    if _, err := publish.Handle(ctx, bob, item.ID); !errors.Is(err, article.ErrForbidden) {
        t.Fatalf("bob published: %v", err)
    }
    if _, err := publish.Handle(ctx, alice, item.ID); err != nil {
        t.Fatal(err)
    }
    if _, err := publish.Handle(ctx, alice, item.ID); !errors.Is(err, article.ErrAlreadyPublished) {
        t.Fatalf("published twice: %v", err)
    }
    // 发布之后所有人都能看到
    if _, err := get.Handle(ctx, bob, item.ID); err != nil {
        t.Fatal(err)
    }
    // slug 不能重复
    _, err = create.Handle(ctx, bob, article.Changes{Title: "Another", Slug: "hello-world"})
    if !errors.Is(err, article.ErrSlugTaken) {
        t.Fatalf("duplicate slug: %v", err)
    }
}
```

`dbtest.Open` 为这个测试创建一个独立的数据库并执行 `migrations.FS` 中的迁移，详见下面的[数据库测试](#database-tests)。

<a id="testing-action-commands"></a>
### 测试命令入口

action 的[命令入口](actions.md#as-commands)通过模块的 `Commands` 测试。它返回原生的 `*cobra.Command`，直接传入测试数据库即可，不需要配置文件，也不经过 `command.Bind`。下面是 `internal/article/commands_test.go`：

```go
package article_test

import (
    "bytes"
    "context"
    "testing"

    "example.com/blog/internal/article"
    "example.com/blog/internal/user"
    "example.com/blog/migrations"
    "github.com/ryan053857/mygoravel/command"
    "github.com/ryan053857/mygoravel/database/dbtest"
    "github.com/ryan053857/mygoravel/identity"
)

func TestPublishCommand(t *testing.T) {
    ctx := context.Background()
    db := dbtest.Open(t, migrations.FS)
    alice := identity.Subject{Type: user.Type, ID: "alice"}
    item, err := article.NewCreate(db).Handle(ctx, alice, article.Changes{Title: "Hello World", Slug: "hello-world"})
    if err != nil {
        t.Fatal(err)
    }

    run := func(args ...string) (string, error) {
        var out bytes.Buffer
        cmd := article.Commands(db)
        cmd.SetOut(&out)
        cmd.SetErr(&out)
        cmd.SetArgs(args)
        err := cmd.ExecuteContext(ctx)
        return out.String(), err
    }
    if out, err := run("publish", item.ID.String()); err != nil || out != "hello-world\n" {
        t.Fatalf("out = %q, err = %v", out, err)
    }
    // 业务错误按分类转换为退出码：重复发布是 Conflict，以 1 退出
    if _, err := run("publish", item.ID.String()); command.ExitCode(err) != command.ExitFailure {
        t.Fatalf("exit code = %d, err = %v", command.ExitCode(err), err)
    }
    // 参数不是合法的 ID 时是用法错误
    if _, err := run("publish", "not-a-uuid"); command.ExitCode(err) != command.ExitUsage {
        t.Fatalf("exit code = %d, err = %v", command.ExitCode(err), err)
    }
}
```

命令入口只负责解析参数和输出，业务规则仍然在上面的 `Handle` 测试中覆盖。

优先使用真实的依赖。只有在确实需要隔离某项外部能力时（例如对象存储），才在业务包中定义一个小接口，并在测试中提供简单的实现，见[依赖装配](dependencies.md#replacing-dependencies-in-tests)。

<a id="test-data"></a>
## 准备测试数据

测试数据通过 action 创建，这样数据一定满足业务规则，测试不会用到线上不可能出现的数据。mygoravel 没有 Laravel 那样的模型工厂，许多测试都要用的数据，在测试文件中写一个带默认值的函数，例如 `internal/article/helpers_test.go`：

```go
// newArticle 以 author 的身份创建一篇草稿。edit 修改默认内容，不需要时传 nil。
func newArticle(t *testing.T, db *bun.DB, author identity.Subject, edit func(*article.Changes)) *article.Article {
    t.Helper()
    c := article.Changes{Title: "Hello World", Slug: strings.ToLower(rand.Text()), Content: "……"}
    if edit != nil {
        edit(&c)
    }
    item, err := article.NewCreate(db).Handle(context.Background(), author, c)
    if err != nil {
        t.Fatal(err)
    }
    return item
}
```

每个测试只写出它关心的字段：

```go
item := newArticle(t, db, alice, func(c *article.Changes) { c.Slug = "hello-world" })
```

- 内容用固定的值。只有必须唯一的字段用随机值，例如 slug，这样一个测试可以创建多篇文章。
- 需要已发布的文章时再调用 `Publish`。「两天前发布」这类和时间有关的数据，用下一节的 `synctest` 推进时间得到，不要直接修改数据库中的时间。
- 只有 action 产生不了的数据，例如从旧系统迁移来的文章，才用 `db.NewInsert()` 直接写入。
- 其他包的测试也要创建文章时，把这些函数导出到 `internal/article/articletest` 包中。

<a id="controlling-time"></a>
## 控制时间

和时间有关的测试使用 Go 标准库的 [testing/synctest](https://pkg.go.dev/testing/synctest)，它对应 Laravel 的 `travelTo` 和 `Carbon::setTestNow`。在 `synctest.Test` 中，`time.Now`、`time.Sleep`、定时器和 `context` 的超时都使用一个假时钟：代码执行时时钟不走，`time.Sleep` 立即把时钟拨到之后。不需要给 action 传入时钟。

下面的测试检查草稿放了两天才发布时，发布时间是发布那一刻：

```go
func TestPublishRecordsTime(t *testing.T) {
    synctest.Test(t, func(t *testing.T) {
        ctx := context.Background()
        db := dbtest.Open(t, migrations.FS)
        alice := identity.Subject{Type: user.Type, ID: "alice"}
        item := newArticle(t, db, alice, nil)

        time.Sleep(48 * time.Hour)
        published, err := article.NewPublish(db).Handle(ctx, alice, item.ID)
        if err != nil {
            t.Fatal(err)
        }
        // 时钟只在 Sleep 时移动，所以可以精确比较
        if !published.PublishedAt.Equal(time.Now()) {
            t.Fatalf("published at %v", published.PublishedAt)
        }
    })
}
```

使用时注意：

- 数据库在 `synctest.Test` 中打开。只有 Go 程序中的时钟是假的：查询用到的时间都要由 Go 算出后作为参数传入，例如 `Where("expires_at > ?", time.Now())`，不要用 `NOW()`；迁移中的时间列也不设默认值，见[时间戳](database.md#timestamps)。框架自己的 SQL 都遵守这条规则，由 `internal/checks/clocktest` 检查。
- 由服务端计算的过期时间不受假时钟控制，例如 Redis 的 `EXPIRE`。
- 假时钟从 2000-01-01 00:00 UTC 开始，需要某个具体时刻时用 `time.Sleep(time.Until(target))` 拨过去。
- `synctest.Test` 中不能调用 `t.Run` 和 `t.Parallel`。要在几种情况下测试时，在每个子测试中分别调用 `synctest.Test`。

<a id="testing-configuration"></a>
## 测试配置

把配置内容写进临时文件，用 `t.Setenv` 提供环境变量，再调用 `config.Load`：

```go
func TestLoadConfig(t *testing.T) {
    t.Setenv("HTTP_ADDR", ":9000")
    path := filepath.Join(t.TempDir(), "config.yaml")
    data := "http:\n  addr: ${HTTP_ADDR}\ndatabase:\n  dsn: postgres://localhost/test\n"
    if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
        t.Fatal(err)
    }

    cfg, err := config.Load[settings.Config](path)
    if err != nil {
        t.Fatal(err)
    }
    if cfg.HTTP.Addr != ":9000" {
        t.Fatalf("addr = %q", cfg.HTTP.Addr)
    }
}
```

`t.Setenv` 会在测试结束时恢复环境变量，但使用它的测试不能调用 `t.Parallel`。

建议覆盖以下情况：默认值、显式的零值、未知字段和校验失败。

<a id="database-tests"></a>
## 数据库测试

<a id="creating-test-databases"></a>
### 创建测试数据库

测试数据库代码时，最可靠的方式是使用与生产相同的真实数据库。`dbtest.Open` 会为**每个测试**创建一个独立的临时数据库，执行你的迁移，并在测试结束时删除它。测试之间互不干扰，可以放心地并行运行。

上一节的 `article_test.go` 就是这样使用它的：

```go
db := dbtest.Open(t, migrations.FS)
```

`dbtest.Open` 返回一个已执行迁移的 `*bun.DB`，你可以把它直接传给 action 的构造函数。迁移参数传 `nil` 时会创建一个空数据库。

`dbtest` 通过环境变量 `POSTGRES_TEST_URL` 获取一个管理员连接，该账户需要有创建和删除数据库的权限。没有设置这个变量时，测试会直接失败，而不是被跳过——这样可以避免数据库测试在 CI 中被悄悄忽略。

> [!WARNING]
> 请为这个变量使用专门的测试数据库实例，不要指向任何存放真实数据的数据库。

<a id="running-database-tests-locally"></a>
### 在本地运行数据库测试

在项目根目录放一个只给测试用的 `compose.test.yml`，启动应用用到的 PostgreSQL、[Redis](redis.md#testing) 和 [Memcached](cache.md#testing)：

```yaml
# compose.test.yml
name: blog-test

services:
  postgres:
    image: postgres:18
    environment:
      POSTGRES_PASSWORD: postgres
    ports:
      - 127.0.0.1:55432:5432
    tmpfs:
      - /var/lib/postgresql
    healthcheck:
      test: pg_isready -U postgres
      interval: 1s
      retries: 60

  redis:
    image: redis:8
    ports:
      - 127.0.0.1:56379:6379
    tmpfs:
      - /data
    healthcheck:
      test: redis-cli ping
      interval: 1s
      retries: 30

  memcached:
    image: memcached:1.6-alpine
    ports:
      - 127.0.0.1:51211:11211
    healthcheck:
      test: nc -z 127.0.0.1 11211
      interval: 1s
      retries: 30
```

`--wait` 等到所有服务的健康检查通过才返回，之后就可以直接运行测试：

```bash
docker compose -f compose.test.yml up -d --wait
export POSTGRES_TEST_URL='postgres://postgres:postgres@127.0.0.1:55432/postgres?sslmode=disable'
export REDIS_TEST_URL='redis://127.0.0.1:56379/0'
export MEMCACHED_TEST_ADDR='127.0.0.1:51211'
go test -race -count=1 ./...
```

每个测试使用自己的临时库，服务可以一直开着反复运行测试。数据放在 tmpfs 中，执行 `docker compose -f compose.test.yml down -v` 清理后不会留下任何数据。

Redis 的集成测试见 [Redis](redis.md#testing)。

<a id="command-tests"></a>
## 命令测试

测试命令行时，不需要启动新的进程。`cli.New` 返回的是原生的 `*cobra.Command`，用 Cobra 的 `SetOut`、`SetArgs` 把输出重定向到 `bytes.Buffer` 并指定参数，然后执行命令并检查输出和退出码。下面是博客应用完整的 `internal/cli/cli_test.go`，它测试的是[命令行](commands.md#declaring-the-cli)一章中真实的 `cli.New`：

```go
package cli_test

import (
    "bytes"
    "context"
    "path/filepath"
    "strings"
    "testing"

    "example.com/blog/internal/cli"
    "github.com/ryan053857/mygoravel/command"
)

func run(args ...string) (string, error) {
    var out bytes.Buffer
    root := cli.New()
    root.SetOut(&out)
    root.SetErr(&out)
    root.SetArgs(args)
    err := root.ExecuteContext(context.Background())
    return out.String(), err
}

func TestOpenAPIDoesNotNeedConfig(t *testing.T) {
    out, err := run("openapi")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(out, `"/articles/{id}/publish"`) {
        t.Fatalf("missing route in %s", out)
    }
}

func TestRoutesFilters(t *testing.T) {
    out, err := run("routes", "--method", "post", "--path", "/articles")
    if err != nil {
        t.Fatal(err)
    }
    if !strings.Contains(out, "article-create") || !strings.Contains(out, "article-publish") {
        t.Fatalf("missing route in %s", out)
    }
    if strings.Contains(out, "article-get") || strings.Contains(out, "/docs") {
        t.Fatalf("route filters did not apply: %s", out)
    }
}

func TestConfigCheckReportsMissingFile(t *testing.T) {
    _, err := run("config", "check", "--config", filepath.Join(t.TempDir(), "missing.yaml"))
    if command.ExitCode(err) != command.ExitConfig {
        t.Fatalf("exit code = %d, err = %v", command.ExitCode(err), err)
    }
}
```

前两个测试确认 `openapi`、`routes` 命令可以离线查看真实的接口声明，不需要配置文件；最后一个测试确认配置文件缺失时，`config check` 以 `ExitConfig` 退出。

<a id="testing-shutdown"></a>
## 测试平滑停止

如果你的应用对停机行为有要求，可以为 `httpx.Serve` 编写测试，覆盖以下场景：

- 取消 context 后，服务正常退出；
- 停机时正在处理的请求能够完成；
- 排空超时后，未完成的请求被取消；
- 监听发生错误时，`Serve` 返回该错误。

如果入口自己处理系统信号，还可以在测试中以子进程方式启动应用，发送 `SIGTERM`，验证资源被正确关闭以及进程的退出码。

<a id="testing-openapi"></a>
## 测试 OpenAPI 文档

生成文档时，路由用 `nil` 依赖注册，处理器不会被调用。用应用的 [DocRoutes](dependencies.md#assembling-the-application) 注册全部路由并生成文档，可以确保没有哪个构造函数在注册时使用了依赖，否则测试会 panic：

```go
func TestOpenAPI(t *testing.T) {
    opts := bootstrap.API
    opts.Mode = rest.ModeOpenAPI
    api := rest.New(opts)
    bootstrap.DocRoutes(api)

    data, err := api.OpenAPIJSON()
    if err != nil {
        t.Fatal(err)
    }
    // 与仓库中的 openapi.json 比较……
}
```

你还可以把生成的文档与仓库中保存的 `openapi.json` 进行比较，这样每次接口变更都会在代码审查中清晰可见。
