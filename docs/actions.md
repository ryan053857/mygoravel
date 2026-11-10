# 业务动作

- [简介](#introduction)
- [编写 action](#writing-actions)
- [模块内共享的代码](#shared-code)
- [从不同入口运行](#entry-points)
    - [HTTP 接口](#as-http)
    - [队列任务](#as-jobs)
    - [定时任务](#as-scheduled-tasks)
    - [命令、页面和其他 action](#direct-calls)
- [数据对象](#data-objects)
- [约定](#conventions)

<a id="introduction"></a>
## 简介

mygoravel 推荐把业务逻辑写成 **action**：每个业务动作是一个类型，单独放在一个文件中，例如“发布文章”就是 `internal/article/publish.go` 中的 `article.Publish`。这个文件包含这个动作的全部代码：依赖、业务规则，以及 HTTP 接口、队列任务等各个入口。

这个模式借鉴自 Laravel 的 [laravel-actions](https://github.com/lorisleiva/laravel-actions)，但不需要任何框架支持：没有 `Action` 接口，没有反射，也不靠方法名约定自动发现。入口方法就是普通的 Go 方法，在注册路由或任务时作为方法值传进去，签名由编译器检查。

模块中没有一个包揽所有操作的 `Service`。需要修改“发布文章”时，只看 `publish.go` 一个文件就够了；新增一个动作就新增一个文件，不会让某个类型越来越大。

<a id="writing-actions"></a>
## 编写 action

下面是完整的 `internal/article/publish.go`：

```go
package article

import (
    "context"
    "database/sql"
    "time"

    "github.com/cockroachdb/errors"
    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/identity"
)

// Publish 发布文章。
type Publish struct {
    db *bun.DB
}

func NewPublish(db *bun.DB) *Publish {
    return &Publish{db: db}
}

// Handle 发布一篇草稿：只有作者能发布，不能重复发布。
func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := a.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
        err := tx.NewSelect().Model(item).Where("id = ?", id).For("UPDATE").Scan(ctx)
        if errors.Is(err, sql.ErrNoRows) {
            return ErrNotFound.New()
        }
        if err != nil {
            return errors.Wrap(err, "load article")
        }
        if !canEdit(subject, item) {
            return ErrForbidden.New()
        }
        if item.Status == StatusPublished {
            return ErrAlreadyPublished.New()
        }
        now := time.Now().UTC()
        item.Status, item.PublishedAt = StatusPublished, &now
        _, err = tx.NewUpdate().Model(item).Column("status", "published_at", "updated_at").WherePK().Exec(ctx)
        return errors.Wrap(err, "publish article")
    })
    if err != nil {
        return nil, err
    }
    return item, nil
}

type PublishInput struct {
    ID uuid.UUID `path:"id"`
}

// HTTP 处理 POST /articles/{id}/publish。
func (a *Publish) HTTP(ctx context.Context, in *PublishInput) (*ArticleOutput, error) {
    subject, _ := identity.FromContext(ctx)
    item, err := a.Handle(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return &ArticleOutput{Body: toDTO(item)}, nil
}
```

`Handle` 是动作本身。它只接收业务参数，调用者的身份作为参数 `subject` 显式传入，不从 context 中读取，因此命令行、队列和测试都能直接调用它，见[授权](authorization.md#ownership)。

`HTTP` 是这个动作的 HTTP 入口：它从输入类型中取出参数、从 context 中取出主体，调用 `Handle`，再把结果转换为响应。它不包含业务规则，`Handle` 返回的业务错误原样返回，由框架转换为对应的状态码，见[错误处理](errors.md)。

<a id="shared-code"></a>
## 模块内共享的代码

多个 action 共用的东西放在 `article.go` 中：模型、业务错误、提交的数据，以及包内的查询和授权函数：

```go
package article

// Article 的定义见数据库一章。

var (
    ErrNotFound         = errorx.Define(errorx.NotFound, "article.not_found")
    ErrForbidden        = errorx.Define(errorx.PermissionDenied, "article.forbidden")
    ErrSlugLocked       = errorx.Define(errorx.InvalidArgument, "article.slug_locked")
    ErrSlugTaken        = errorx.Define(errorx.Conflict, "article.slug_taken")
    ErrAlreadyPublished = errorx.Define(errorx.Conflict, "article.already_published")
)

// Changes 是创建和修改文章时提交的内容。
type Changes struct {
    Title   string `json:"title" minLength:"1" maxLength:"200"`
    Slug    string `json:"slug" maxLength:"100" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" patternDescription:"小写字母、数字和连字符"`
    Content string `json:"content"`
}

// find 按 ID 读取文章。db 可以是 *bun.DB，也可以是事务。
func find(ctx context.Context, db bun.IDB, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := db.NewSelect().Model(item).Where("id = ?", id).Scan(ctx)
    if errors.Is(err, sql.ErrNoRows) {
        return nil, ErrNotFound.New()
    }
    if err != nil {
        return nil, errors.Wrap(err, "load article")
    }
    return item, nil
}

func canEdit(subject identity.Subject, item *Article) bool {
    return subject.Can("articles:admin") || item.AuthorID == subject.ID
}
```

模型的定义见[定义模型](database.md#defining-models)，业务错误见[定义业务错误](errors.md#defining-errors)。`find` 和 `canEdit` 不导出，它们只是 action 之间共享的实现细节。不要因为两个 action 有几行相似的代码就急着提取函数；规则真正相同时再提取。

<a id="entry-points"></a>
## 从不同入口运行

同一个 action 可以从多个入口运行。每个入口是 action 上的一个方法，在注册的地方作为方法值传入。

<a id="as-http"></a>
### HTTP 接口

模块的 `routes.go` 声明路由，把 action 的 `HTTP` 方法注册为处理器：

```go
publish := huma.Operation{
    OperationID: "article-publish",
    Method:      http.MethodPost,
    Path:        "/articles/{id}/publish",
    Summary:     "发布文章",
}
requireArticles(&publish)
rest.Register(api, publish, NewPublish(db).HTTP)
```

`routes.go` 的完整代码见[模块内注册](routing.md#module-registration)。action 在注册时构造一次，之后它的方法被所有请求并发调用。

<a id="as-jobs"></a>
### 队列任务

定时发布一篇文章时，把文章 ID 作为任务发布到队列，到时间后由 worker 调用 `Publish`。在 `publish.go` 中加入任务的 topic 和处理方法：

```go
// PublishAt 是定时发布的任务，payload 是文章 ID。
var PublishAt = queue.Topic[uuid.UUID]("articles.publish")

// system 是队列任务的调用者，canEdit 允许它发布任何文章。
var system = identity.Subject{ID: "system", Type: identity.SubjectService, Permissions: []string{"articles:admin"}}

// Job 处理定时发布的任务。文章已经发布时任务视为完成，所以重复执行是安全的。
func (a *Publish) Job(ctx context.Context, id uuid.UUID) error {
    _, err := a.Handle(ctx, system, id)
    switch {
    case errors.Is(err, ErrAlreadyPublished):
        return nil
    case errors.Is(err, ErrNotFound):
        return queue.Permanent(err) // 文章已被删除，不再重试
    }
    return err
}
```

`queue.Topic` 把 topic 名称和 payload 类型绑在一起，发布和处理共用这一个声明。设置定时发布的 action 在写入发布时间的同一个事务中发布任务：

```go
err := PublishAt.Publish(ctx, queue.Database(tx), item.ID, queue.At(publishAt))
```

worker 注册处理方法，见[启动 worker](queue.md#running-workers)：

```go
article.PublishAt.Handle(router, sub, article.NewPublish(a.db).Job)
```

<a id="as-scheduled-tasks"></a>
### 定时任务

调度器的任务是 `func(ctx context.Context) error`。`Handle` 正好是这个签名的 action 可以直接注册，例如[定时任务](scheduling.md#defining-tasks)一章中发布到期文章的 `PublishScheduled`：

```go
sched.Add("articles:publish-scheduled", "* * * * *", article.NewPublishScheduled(a.db).Handle)
```

<a id="direct-calls"></a>
### 命令、页面和其他 action

其他地方直接调用 `Handle`。[填充数据](migrations.md#seeding)的命令用 `Create` 和 `Publish` 写入示例文章，[页面处理器](views.md#rendering-pages)持有它用到的 action：

```go
item, err := article.NewCreate(db).Handle(ctx, alice, changes)
```

页面处理器不放在 `article` 包中，因为 templ 组件要导入 `article` 包的类型，放在一起会造成循环导入。

一个 action 需要另一个 action 时，通过构造函数接收它，而不是在内部构造。例如导入文章时复用 `Create` 的规则：

```go
type Import struct {
    create *Create
}

func NewImport(create *Create) *Import {
    return &Import{create: create}
}
```

<a id="data-objects"></a>
## 数据对象

Laravel 的 [laravel-data](https://github.com/spatie/laravel-data) 用一个类同时描述验证规则、请求数据和响应数据。在 mygoravel 中，这些由普通的 Go 结构体完成：

| 用途 | 写法 |
| --- | --- |
| 验证并读取请求 | 带验证标签的结构体，例如 `Changes`；HTTP 输入类型写成 `Body Changes`，见[请求](requests.md#json-body) |
| 页面表单 | 同一个结构体：`web.ParseForm[article.Changes](r)`，见[表单](forms.md) |
| 业务参数 | `Handle` 直接接收 `Changes`，验证标签对它没有影响 |
| 响应 | `ArticleDTO` 和转换函数 `toDTO`，见[共享响应结构](routing.md#shared-responses) |
| 前端类型 | 由导出的 [OpenAPI 文档](routing.md#exporting-openapi)生成，例如用 openapi-typescript |

`Changes` 只在一处声明，JSON 接口、页面表单和业务代码都使用它，验证规则不会在几个地方写得不一致。转换函数是普通的函数，字段改名时编译器会发现遗漏。

<a id="conventions"></a>
## 约定

- **命名**：类型名是动词，包名已经说明了对象，例如 `article.Publish`、`article.Create`；文件名与类型名对应，例如 `publish.go`。
- **构造函数只保存依赖**。生成 OpenAPI 文档和[查看路由](routing.md#listing-routes)时，路由照常注册，action 会被构造，但传入的依赖都是 `nil`，方法不会被调用。
- **字段只保存依赖**。action 被并发调用，请求数据只放在参数和局部变量中。
- **`Handle` 是唯一的业务入口**。入口方法只做转换：读取参数和主体、调用 `Handle`、转换结果和错误。
- **测试 `Handle`**。业务规则直接调用 `Handle` 测试，入口的认证和验证通过 HTTP 测试覆盖，见[测试](testing.md#testing-actions)。

这些是推荐的约定，框架不检查它们。简单的接口可以直接用函数作为处理器，见[基本路由](routing.md#basic-routing)。
