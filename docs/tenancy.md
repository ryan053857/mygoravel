# 多租户

- [简介](#introduction)
- [确定租户](#identifying-tenants)
- [成员与角色](#members)
    - [创建租户](#creating-tenants)
    - [成员管理](#managing-members)
    - [切换租户](#switching-tenants)
- [租户数据隔离](#data-isolation)
- [后台任务](#background-work)
    - [队列任务](#tenant-jobs)
    - [定时任务](#tenant-scheduled-tasks)
- [缓存、广播与其他资源](#other-resources)
- [运营后台](#platform)
- [租户的客户](#customers)
- [令牌与租户](#tokens)

<a id="introduction"></a>
## 简介

SaaS 应用通常有三类用户：

| 用户 | 例子 | 在 mygoravel 中 |
| --- | --- | --- |
| 运营人员 | 平台的客服、管理员 | 在平台本身（域 `""`）中拥有角色 |
| 租户成员 | 某个公司的员工 | 在该租户中拥有角色，同一个人可以属于多个租户 |
| 租户的客户 | 公司的终端客户 | 同样在租户中拥有角色，例如 `customer` |

所有租户的数据存在同一个数据库中，用 `tenant_id` 区分。框架不提供按租户切换数据库：会话、队列、令牌和角色都只有一份，同一个库就是最简单、也最不容易出错的结构；需要更强的隔离时，用 PostgreSQL 的[行级安全策略](#data-isolation)兜底。实现多租户需要三件事：

1. 从请求中**确定租户**；
2. 按用户在这个租户中的**角色**加载权限；
3. 业务代码只读写**这个租户的数据**。

前两件由框架和 [`authz`](authorization.md#roles) 完成，第三件是 [action](actions.md) 必须遵守的约定。

<a id="identifying-tenants"></a>
## 确定租户

本次请求属于哪个租户由请求本身决定。用 `Tenant` 选项告诉 API 和页面怎样取出它，常见的三种方式：

```go
// 路径：/t/{tenant}/articles
api := rest.New(rest.Options{
    Tenant:  func(ctx huma.Context) string { return ctx.Param("tenant") },
    Resolve: roles.Resolve,
})
pages := web.New(web.Options{
    Tenant:  func(r *http.Request) string { return chi.URLParam(r, "tenant") },
    Resolve: roles.Resolve,
    // ...
})

// 子域名：acme.example.com；主域名 example.com 是平台本身
Tenant: func(r *http.Request) string {
    if tenant, ok := strings.CutSuffix(r.Host, ".example.com"); ok {
        return tenant
    }
    return ""
},

// 请求头：适合只有 API 的应用
Tenant: func(ctx huma.Context) string { return ctx.Header("X-Tenant") },
```

取出的租户在每个请求开始时写入 context，公开的路由也一样，业务代码用 `identity.TenantFromContext` 读取。取不到时租户是 `""`，即平台本身。租户使用自定义域名、需要查表才能确定时，在 chi 中间件中完成查找并拒绝未知的域名，`Tenant` 只读取查找结果。

`roles.Resolve` 加载用户在这个租户中的权限，并写入 `sub.Tenant`。用户在这个租户中没有任何角色时返回 `404`：不是成员的人无法分辨这个租户是否存在。

<a id="members"></a>
## 成员与角色

用户在租户中的角色就是他的成员身份：有角色即是成员，撤销全部角色即移出租户。

角色分两种：

- **全局角色**（`Tenant` 为 `""`）：`owner`、`admin`、`member` 这类模板，所有租户都可以分配，修改它会影响所有租户；
- **租户角色**（`Tenant` 为租户 ID）：租户管理员按需创建，只在本租户中可见、可分配。

全局角色通常在[填充数据](migrations.md#seeding)时创建：

```go
owner, err := roles.CreateRole(ctx, authz.Role{Name: "owner", Permissions: []string{"*"}})
member, err := roles.CreateRole(ctx, authz.Role{Name: "member", Permissions: []string{auth.ArticlesRead, auth.ArticlesWrite}})
```

<a id="creating-tenants"></a>
### 创建租户

租户本身是应用的数据，例如一张 `tenants` 表。用户创建组织时，在同一个 action 中把他设为所有者：

```go
func (a *Create) Handle(ctx context.Context, subject identity.Subject, name string) (*Tenant, error) {
    t, err := insert(ctx, a.db, name)
    if err != nil {
        return nil, err
    }
    return t, a.roles.Assign(ctx, subject.ID, t.ID, a.ownerRoleID)
}
```

<a id="managing-members"></a>
### 成员管理

成员管理页用 `members:manage` 保护，所有操作都限定在本次请求的租户 `sub.Tenant` 中：

| 操作 | 调用 |
| --- | --- |
| 列出成员和他们的角色 | `roles.Members(ctx, sub.Tenant)` |
| 可以分配的角色 | `roles.Roles(ctx, sub.Tenant)`：全局角色加本租户的角色 |
| 邀请成员、调整角色 | `roles.Assign`、`roles.Unassign` |
| 移出成员 | `roles.RemoveMember(ctx, userID, sub.Tenant)` |
| 自定义角色 | `roles.CreateRole(ctx, authz.Role{Tenant: sub.Tenant, ...})` |
| 读取、修改某个角色 | 先用 `roles.Role(ctx, id, sub.Tenant)` 确认它属于本租户或是全局角色 |

调整角色或移出成员之后，调用 [`RecheckSubject`](sse.md#immediate-revocation) 让这个人已经打开的长连接立即按新的权限校验。

**租户 ID 只能来自 `sub.Tenant`，不能来自表单或请求体**，否则管理员可以操作其他租户。`Assign` 会拒绝把其他租户的角色分配到本租户，但无法知道调用者是否有权管理这个租户，这由路由上的 `members:manage` 和 `sub.Tenant` 保证。

全局角色中可能有只供运营使用的角色。租户的角色列表中不应出现它们时，在展示和分配前按名称过滤。即使分配了，权限也只在这个租户中生效，不会扩散到平台或其他租户。

<a id="switching-tenants"></a>
### 切换租户

`roles.Tenants(ctx, userID)` 返回用户所属的全部租户，用于登录后的租户选择页或顶部的切换菜单。切换只是跳转到另一个租户的地址，例如从 `/t/acme` 到 `/t/globex`，会话和令牌都不需要改变。

<a id="data-isolation"></a>
## 租户数据隔离

属于租户的每张表都有 `tenant_id` 列，唯一约束和常用索引都以它开头：

```sql
CREATE TABLE articles (
    id uuid PRIMARY KEY,
    tenant_id text NOT NULL,
    slug text NOT NULL,
    -- ...
    CONSTRAINT articles_tenant_slug_unique UNIQUE (tenant_id, slug)
);
CREATE INDEX articles_tenant_created ON articles (tenant_id, created_at);
```

action 从主体中取租户，每一个查询都加上这个条件，写入时也从主体中取。模块内共享的 `find` 接收主体，而不是单独的租户 ID：

```go
func find(ctx context.Context, db bun.IDB, subject identity.Subject, id uuid.UUID) (*Article, error) {
    item := new(Article)
    err := db.NewSelect().Model(item).
        Where("tenant_id = ?", subject.Tenant).Where("id = ?", id).Scan(ctx)
    if errors.Is(err, sql.ErrNoRows) {
        return nil, ErrNotFound.New() // 其他租户的文章与不存在的文章无法区分
    }
    // ……
}

func (a *Create) Handle(ctx context.Context, subject identity.Subject, c Changes) (*Article, error) {
    item := &Article{TenantID: subject.Tenant, AuthorID: subject.ID /* ... */}
    // ……
}
```

这是多租户应用中最重要的约定：漏掉一个条件，就会把一个租户的数据暴露给另一个租户。几个习惯可以降低风险：

- `Handle` 和模块内的查询函数总是接收 `identity.Subject`，不单独接收租户 ID；
- 按 ID 查询时也带上 `tenant_id`，不要先按 ID 查出来再比较；
- 为每个 action 写一个“其他租户访问不到”的测试，例如用租户 B 的主体读取租户 A 的文章，期望 `404`，见[测试 action](testing.md#testing-actions)；
- 使用 PostgreSQL 时，可以再加上[行级安全策略](https://www.postgresql.org/docs/current/ddl-rowsecurity.html)作为兜底：在事务开始时 `SET LOCAL app.tenant_id`，由策略限制每个查询只能看到这个租户的行。

<a id="background-work"></a>
## 后台任务

请求中的租户来自 `Tenant` 选项，队列和定时任务没有请求，租户只能由任务自己带着。worker 不会恢复任何上下文：action 从参数 `subject` 中取租户，所以只要构造出带租户的主体，`Handle` 的写法与请求中完全相同。

<a id="tenant-jobs"></a>
### 队列任务

[定时发布](actions.md#as-jobs)的任务在单租户应用中只带文章 ID。多租户应用中，`find` 按 `subject.Tenant` 查询，只带 ID 的任务会以平台本身的身份去找文章，结果永远是 `ErrNotFound`。payload 要带上租户，系统主体按租户构造：

```go
// PublishJob 是定时发布的任务。
type PublishJob struct {
    Tenant string    `json:"tenant"`
    ID     uuid.UUID `json:"id"`
}

var PublishAt = queue.Topic[PublishJob]("articles.publish")

// system 是队列任务在 tenant 中的调用者，CanEdit 允许它发布这个租户的任何文章。
func system(tenant string) identity.Subject {
    return identity.Subject{ID: "system", Type: identity.SubjectService, Tenant: tenant, Permissions: []string{"articles:admin"}}
}

func (a *Publish) Job(ctx context.Context, job PublishJob) error {
    _, err := a.Handle(ctx, system(job.Tenant), job.ID)
    // ……与单租户相同
}
```

发布任务时，租户同样取自主体：

```go
err := PublishAt.Publish(ctx, queue.Database(tx), PublishJob{Tenant: subject.Tenant, ID: item.ID}, queue.At(publishAt))
```

用 [queue.Unique](queue.md#unique-jobs) 合并任务时，键只在 topic 内唯一，ID 不是全局唯一的话，键中要带上租户。

系统主体的权限只在 `job.Tenant` 中生效，即使 payload 中的文章 ID 属于其他租户，`find` 也找不到它。[操作日志](activity.md)按主体的租户记录，队列中的操作因此也落在正确的租户里。命令行入口同理，用 `--tenant` 参数构造 `system(tenant)`。

<a id="tenant-scheduled-tasks"></a>
### 定时任务

[发布到期文章](scheduling.md#defining-tasks)的 `PublishScheduled` 用一条 `UPDATE` 处理所有到期的文章，不区分租户，也不需要主体，多租户时不用改。

需要逐个租户执行的任务，例如每天给每个租户发一份摘要，不要在定时任务里循环处理：一个租户出错会中断其他租户，执行时间也随租户数量增长。定时任务只为每个租户发布一个队列任务，由 worker 并行处理，各自重试：

```go
// SendDigest 是一个租户的每日摘要，payload 是租户 ID。
var SendDigest = queue.Topic[string]("digest.send")

func (a *ScheduleDigests) Handle(ctx context.Context) error {
    return database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        var ids []string
        if err := tx.NewSelect().Table("tenants").Column("id").Scan(ctx, &ids); err != nil {
            return err
        }
        for _, id := range ids {
            if err := SendDigest.Publish(ctx, queue.Database(tx), id); err != nil {
                return err
            }
        }
        return nil
    })
}
```

所有任务在一个事务中发布，要么全部发布，要么都不发布。

<a id="other-resources"></a>
## 缓存、广播与其他资源

数据库之外，凡是按名称共享的东西都要考虑租户。原则只有一条：**名称中带上租户，租户取自 `sub.Tenant`**。

| 资源 | 做法 |
| --- | --- |
| [缓存](cache.md) | 键带上租户：`blog:article:v1:<tenant>:<id>`。缓存一章的 `load` 只按 ID 读取，命中后会跳过 `find` 的租户条件，把其他租户的文章返回出去；它要和 `find` 一样接收主体，用 `subject.Tenant` 组成键 |
| [广播](broadcasting.md) | 主题带上租户：`t:<tenant>:articles`。SSE 处理器用 `sub.Tenant` 订阅，不要从查询参数中取租户；[实时频道](realtime.md#private-channels)的授权函数把频道名中的租户与 `sub.Tenant` 比较 |
| [限流](rate-limiting.md) | 按用户或 IP 的限流不用改；按租户限额时，key 用 `tenant:<id>` |
| [锁](locks.md) | 锁名中的 ID 全局唯一（如 UUID）时不用改；按 slug 等租户内唯一的值加锁时，带上租户 |
| [Webhook](webhooks.md) | 不用改。端点属于租户，`webhook.Send` 只发给 `subject.Tenant` 的端点 |
| [文件](filesystem.md) | 不用改。文件记录的是上传者，引用文件的业务行（如文章的封面）有 `tenant_id`，读写都经过业务行的租户检查 |


<a id="platform"></a>
## 运营后台

运营后台挂在不带租户的路径下，租户为 `""`。运营人员的角色分配在平台本身：

```go
platformAdmin, err := roles.CreateRole(ctx, authz.Role{Name: "platform-admin", Permissions: []string{"platform:*"}})
err = roles.Assign(ctx, staffID, "", platformAdmin.ID)
```

平台和租户的权限互不相通：在平台中的角色不会带来任何租户中的权限，反之亦然。

客服需要进入租户排查问题时，在所有租户（`authz.AllTenants`）中给他分配一个只读的全局角色，他在每个租户中都拥有这个角色的权限：

```go
support, err := roles.CreateRole(ctx, authz.Role{Name: "support", Permissions: []string{auth.ArticlesRead, auth.ArticlesReadDrafts}})
err = roles.Assign(ctx, staffID, authz.AllTenants, support.ID)
```

`AllTenants` 中只能分配全局角色。它的权限很大，应当只授予只读权限，并记录访问日志。

需要看到某个用户眼中的页面、或者替他完成操作时，使用[代为登录](authentication.md#impersonation)：客服以这个用户的身份和权限操作，`Impersonator` 记下客服本人。

<a id="customers"></a>
## 租户的客户

租户的终端客户也是主体，在租户中分配一个只有客户权限的角色即可，例如 `customer`。客户账号有两种设计：

- **平台统一账号**：客户注册一次，可以成为多个租户的客户，和租户成员的处理完全相同；
- **租户内账号**：每个租户的客户是独立的账号，同一个邮箱在两个租户中各注册一次。登录时把租户写进主体，凭据就只能在这个租户中使用：

```go
sub := identity.Subject{ID: customer.ID, Type: identity.SubjectUser, Tenant: identity.TenantFromContext(ctx)}
err := pages.Login(r, sub)                   // 页面
plain, err := tokens.Login(ctx, sub, device) // 单页应用或 App
```

带着这样的会话或令牌访问其他租户时，请求返回 `403`。租户内账号的唯一约束应当是 `(tenant_id, email)`，第三方登录的[账号关联](social-login.md#linking-accounts)也要加上租户。

<a id="tokens"></a>
## 令牌与租户

登录令牌不绑定租户，用户可以带着同一个令牌访问他所属的每个租户。为脚本或集成创建的个人访问令牌通常只应该访问一个租户：

```go
plain, err := tokens.Create(ctx, sub, apitoken.Personal{
    Name:   "acme 报表",
    Scopes: []string{auth.ArticlesRead},
    Tenant: sub.Tenant,
})
```

令牌能做的是用户在该租户中的权限与 `Scopes` 的交集，见 [API 令牌](api-tokens.md#personal-access-tokens)。
