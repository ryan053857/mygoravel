# 授权

- [简介](#introduction)
- [权限](#permissions)
    - [在路由上声明](#route-permissions)
    - [在代码中检查](#checking-permissions)
- [角色](#roles)
    - [声明权限目录](#permission-catalog)
    - [创建和分配角色](#creating-roles)
    - [每次请求加载权限](#resolving-permissions)
    - [超级管理员](#super-admins)
- [资源级授权：所有权](#ownership)
    - [编写授权函数](#authorization-functions)
    - [在事务中检查](#checking-in-transactions)
    - [缓存与授权](#caching-and-authorization)
- [列表查询中的授权](#scoping-queries)
- [在页面中按权限显示](#authorization-in-views)
- [授权失败的响应](#authorization-failures)

<a id="introduction"></a>
## 简介

[认证](authentication.md)确认了调用者是谁，接下来还需要回答：这个调用者**能不能**执行当前的操作？这就是授权。

mygoravel 的授权分为三层：

| 层次 | 回答的问题 | 由谁检查 |
| --- | --- | --- |
| 路由 | 能不能使用这一类功能，例如“编辑文章” | 框架，按路由声明的权限，在调用处理器之前 |
| 资源 | 能不能操作这一条数据，例如“只能修改自己的文章” | [action](actions.md) 的 `Handle`，在读写数据的事务中 |
| 数据范围 | 列表里应该出现哪些数据 | action 的 `Handle`，在构造查询时 |

三层缺一不可：路由挡住明显不该访问的请求，资源级检查保证每一条数据只被正确的人修改，数据范围保证列表和搜索不会泄露别人的数据。

<a id="permissions"></a>
## 权限

权限是一个以冒号分隔的字符串，通常是“资源:操作”，例如 `articles:read`、`articles:write`、`members:manage`。主体的 `Permissions` 列出它拥有的权限，`sub.Can(perm)` 判断能否执行某项操作。授予时可以用 `*` 作为最后一段：`articles:*` 包含所有文章权限，单独的 `*` 包含全部权限。

主体的权限从哪里来，取决于应用的规模：简单的应用可以在校验器或登录时直接给出；需要角色、需要随时调整权限的应用使用 `authz` 包，见[角色](#roles)。凭据还可以进一步限制权限，例如只读的个人访问令牌，`Can` 会同时检查两者，见[认证](authentication.md#key-concepts)。

<a id="route-permissions"></a>
### 在路由上声明

JSON API 在 `rest.RequireBearer` 中列出路由需要的权限，页面在 `web.Require` 中列出：

```go
requireWrite := rest.RequireBearer(api, tokens.Validate, "articles:write")

pages.Routes(r, web.Require("articles:write")).Put("/admin/articles/{id}", pages.Handle(h.Update))
```

传入多个权限时，调用者必须全部具备；缺少任何一个时，API 返回 `403`，页面呈现 `403` 错误页。路由需要的权限会出现在 OpenAPI 文档的 `x-required-permissions` 字段和 [`routes` 命令](commands.md)的输出中。

在路由上检查的永远是**权限**，而不是角色名：角色可以由租户自己创建和命名，代码中写死的角色名在那时就会失效。

<a id="checking-permissions"></a>
### 在代码中检查

同一个接口有时需要根据权限返回不同的内容，这时在 `Handle` 中调用 `Can`：

```go
if subject.Can("articles:read-drafts") {
    // 编辑可以看到所有作者的草稿
}
```

<a id="roles"></a>
## 角色

`authz` 包提供基于角色的权限，对应 Laravel 的 spatie/laravel-permission：权限组成角色，角色分配给用户。权限变化立即生效，不需要重新登录。它把数据存在应用的数据库中，支持 PostgreSQL、SQLite 和 MySQL。

使用前先发布它的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "authz", FS: authz.Migrations()},
))
```

<a id="permission-catalog"></a>
### 声明权限目录

应用的全部权限在代码中声明一次。下面是博客的 `internal/auth/permissions.go`：

```go
package auth

import "github.com/ryan053857/mygoravel/authz"

const (
    ArticlesRead       = "articles:read"
    ArticlesReadDrafts = "articles:read-drafts"
    ArticlesWrite      = "articles:write"
    ArticlesAdmin      = "articles:admin"
    CommentsDelete     = "comments:delete"
    MembersManage      = "members:manage"
)

var Permissions = authz.Catalog{
    {Name: ArticlesRead, Description: "查看文章"},
    {Name: ArticlesReadDrafts, Description: "查看所有人的草稿"},
    {Name: ArticlesWrite, Description: "撰写和修改自己的文章"},
    {Name: ArticlesAdmin, Description: "修改和发布任何人的文章"},
    {Name: CommentsDelete, Description: "删除评论"},
    {Name: MembersManage, Description: "管理成员"},
}
```

```go
roles := authz.NewStore(a.db, auth.Permissions)
```

角色只能包含目录中的权限，以及能匹配目录中权限的通配符，写错权限名会在保存角色时得到错误，而不是在运行时悄悄不生效。目录也是角色编辑界面的数据来源：`roles.Catalog()` 返回全部权限和说明。

<a id="creating-roles"></a>
### 创建和分配角色

```go
editor, err := roles.CreateRole(ctx, authz.Role{
    Name:        "editor",
    Description: "编辑",
    Permissions: []string{"articles:*", auth.CommentsDelete},
})

err = roles.Assign(ctx, identity.Ref{Type: user.Type, ID: userID}, "", editor.ID)
```

`Assign` 的第三个参数是**域**：`""` 表示应用本身，多租户应用中是租户 ID，见[多租户](tenancy.md)。一个用户可以有多个角色，权限是它们的合集。

| 方法 | 用途 |
| --- | --- |
| `CreateRole`、`UpdateRole`、`DeleteRole` | 管理角色；修改立即影响拥有该角色的所有用户，删除角色同时撤销它的分配 |
| `Role`、`Roles` | 读取角色，用于角色列表和编辑页 |
| `Assign`、`Unassign` | 分配和撤销角色；重复分配什么也不做 |
| `RolesOf`、`Members` | 某个用户的角色、拥有角色的全部用户 |

初始角色通常在[填充数据](migrations.md#seeding)的命令中创建。

<a id="resolving-permissions"></a>
### 每次请求加载权限

`roles.Resolve` 可以直接作为 API、页面和[实时频道](realtime.md#tokens)的 `Accounts` 中一套账号的 Resolver，它在每个请求中按用户当前的角色加载权限：

```go
accounts := identity.Accounts{user.Type: roles.Resolve}
api := rest.New(rest.Options{Accounts: accounts})
pages := web.New(web.Options{Accounts: accounts /* 其余字段见视图 */})
rt, err := realtime.New(hub, realtime.Options{Authenticate: tokens.Validate, Accounts: accounts})
```

`authz` 按主体的类型和 ID 保存角色，可以同时用于多套账号体系，例如 `identity.Accounts{staff.Type: roles.Resolve, member.Type: roles.Resolve}`。

加载是一次带索引的查询，不使用缓存，因此修改角色、撤销分配都在下一个请求生效。已经建立的长连接按新的权限每 30 秒校验一次；需要立即生效时，修改后调用 `RecheckSubject`，修改角色的权限后调用 `RecheckTenant`，见[立即断开](sse.md#immediate-revocation)。还需要检查账号是否停用时，在外面包一层：

```go
resolve := func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
    u, err := user.Find(ctx, a.db, sub.ID)
    if err != nil {
        return identity.Subject{}, err
    }
    if u.Disabled {
        return identity.Subject{}, auth.ErrUnauthenticated.New() // 401，页面登出
    }
    return roles.Resolve(ctx, sub)
}
```

<a id="super-admins"></a>
### 超级管理员

给角色授予 `*`，它就拥有全部权限，不需要额外的放行逻辑：

```go
admin, err := roles.CreateRole(ctx, authz.Role{Name: "admin", Permissions: []string{"*"}})
```

`*` 只作用于权限检查。资源级的授权函数如果要让管理员也能修改别人的文章，应当检查一个具体的权限，例如下面的 `articles:admin`，而不是判断角色。

<a id="ownership"></a>
## 资源级授权：所有权

具备 `articles:read` 只说明调用者可以使用文章接口，并不意味着他可以读取任意一篇文章。已发布的文章所有人都能看，但草稿只有作者本人能看。“获取文章”的 `Get` action 需要根据当前的主体检查：

```go
var (
    ErrNotFound  = errorx.Define(errorx.NotFound, "article.not_found")
    ErrForbidden = errorx.Define(errorx.PermissionDenied, "article.forbidden")
)

func (a *Get) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    if subject.ID == "" {
        return nil, ErrForbidden.New()
    }
    item, err := find(ctx, a.db, id)
    if err != nil {
        return nil, err
    }
    if item.Status == StatusDraft && item.AuthorID != subject.ID {
        return nil, ErrNotFound.New() // 别人的草稿，当作不存在
    }
    return item, nil
}
```

请注意，主体是作为参数**显式传入** `Handle` 的，而不是在内部从 context 中读取。这样，action 的权限要求一目了然，在测试中也很容易构造不同身份的调用者。`find` 是文章模块中按 ID 查询的函数，见[模块内共享的代码](actions.md#shared-code)。

`Handle` 一开始就拒绝空的主体 ID。这保证了即使 action 被命令行或其他代码直接调用，也不会跳过身份检查。

> [!TIP]
> 上面对别人的草稿返回 `404` 而不是 `403`，这样不会泄露“这篇草稿存在”的信息。而修改别人已发布的文章时，文章本身是公开的，返回 `403` 更准确。你可以根据业务需要选择 `errorx.NotFound` 或 `errorx.PermissionDenied`。

<a id="authorization-functions"></a>
### 编写授权函数

当授权规则变得复杂时，把它提取为业务包 `article.go` 中**导出**的函数，相当于 Laravel 的 Policy。各个 action 和[页面](#authorization-in-views)调用同一个函数，规则只有一份：

```go
// CanEdit 报告 subject 能否修改 item：作者本人，或者能修改任何文章的编辑。
func CanEdit(subject identity.Subject, item *Article) bool {
    return subject.Can("articles:admin") || item.AuthorID == subject.ID
}
```

这些函数只是普通的 Go 函数，不需要注册到任何地方，单元测试也非常简单。

<a id="checking-in-transactions"></a>
### 在事务中检查

对于写操作，请在实际读写数据的**同一个事务**中完成所有权检查。如果先在事务外检查、再在事务内修改，两步之间资源可能已经被其他请求改变。

下面是“发布文章”的 `Publish` action 的 `Handle`，完整的 `publish.go` 见[编写 action](actions.md#writing-actions)。它用 `FOR UPDATE` 锁住这篇文章，在同一个事务中检查权限和状态，然后更新：

```go
func (a *Publish) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        err := tx.NewSelect().Model(item).Where("id = ?", id).For("UPDATE").Scan(ctx)
        if errors.Is(err, sql.ErrNoRows) {
            return ErrNotFound.New()
        }
        if err != nil {
            return errors.Wrap(err, "load article")
        }
        if !CanEdit(subject, item) {
            return ErrForbidden.New()
        }
        if item.Status == StatusPublished {
            return ErrAlreadyPublished.New()
        }
        now := time.Now().UTC()
        item.Status, item.PublishedAt = StatusPublished, &now
        if _, err := tx.NewUpdate().Model(item).Column("status", "published_at", "updated_at").WherePK().Exec(ctx); err != nil {
            return errors.Wrap(err, "publish article")
        }
        return nil
    })
    if err != nil {
        return nil, err
    }
    return item, nil
}
```

> [!NOTE]
> SQLite 不支持 `FOR UPDATE`。使用 `sqlite.Driver` 时，写事务在 `BEGIN` 时就独占了数据库，去掉 `.For("UPDATE")` 即可得到同样的保证，见[使用 SQLite](database.md#sqlite)。

重复发布时返回的 `ErrAlreadyPublished` 与其他文章错误定义在一起，重复发布时客户端会收到 `409`：

```go
var ErrAlreadyPublished = errorx.Define(errorx.Conflict, "article.already_published")
```

<a id="caching-and-authorization"></a>
### 缓存与授权

如果你缓存了资源，从缓存中读取到数据后同样要执行所有权检查，详见[缓存](cache.md#cache-aside)。

<a id="scoping-queries"></a>
## 列表查询中的授权

列表、搜索和导出要在**查询条件**中限定范围，而不是查出来之后再过滤：后者既浪费，又容易在分页、计数时漏掉。下面的文章列表让编辑看到全部文章，其他人只看到已发布的和自己的草稿：

```go
func (a *List) Handle(ctx context.Context, subject identity.Subject, limit int) ([]Article, error) {
    var items []Article
    q := a.db.NewSelect().Model(&items).OrderExpr("created_at DESC").Limit(limit)
    if !subject.Can("articles:read-drafts") {
        q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
            return q.Where("status = ?", StatusPublished).WhereOr("author_id = ?", subject.ID)
        })
    }
    if err := q.Scan(ctx); err != nil {
        return nil, errors.Wrap(err, "list articles")
    }
    return items, nil
}
```

多租户应用中，每个查询还必须限定在 `subject.Tenant` 中，见[租户数据隔离](tenancy.md#data-isolation)。

<a id="authorization-in-views"></a>
## 在页面中按权限显示

页面上的按钮、菜单应当与服务端的规则一致：没有权限的用户不应看到“编辑”按钮。在视图包中写两个小函数，从 context 取出主体（`web.Require` 已经把它放进去了），再调用同一份规则：

```go
package views

func can(ctx context.Context, perm string) bool {
    sub, _ := identity.FromContext(ctx)
    return sub.Can(perm)
}

func canEdit(ctx context.Context, item *article.Article) bool {
    sub, _ := identity.FromContext(ctx)
    return article.CanEdit(sub, item)
}
```

templ 组件中可以直接使用 `ctx`：

```templ
templ ArticleActions(item *article.Article) {
    if canEdit(ctx, item) {
        <a href={ templ.SafeURL("/admin/articles/" + item.ID.String() + "/edit") }>编辑</a>
    }
    if can(ctx, "comments:delete") {
        <button hx-delete={ "/admin/articles/" + item.ID.String() + "/comments" }>清空评论</button>
    }
}
```

隐藏按钮只是为了界面友好，不能代替服务端的检查：action 仍然要按上面的方式授权。

<a id="authorization-failures"></a>
## 授权失败的响应

- 缺少路由所需的权限时，API 返回 `403`，错误码为 `auth.forbidden`，页面呈现 `403` 错误页；
- 在租户中没有任何角色时，`authz` 的 `Resolve` 返回 `authz.not_member`，状态码为 `404`；
- action 返回 `errorx.PermissionDenied` 或 `errorx.NotFound` 分类的错误时，响应对应的状态码，错误码为你定义的业务错误码，例如 `article.forbidden`。

它们都使用统一的[错误响应格式](errors.md#response-format)。
