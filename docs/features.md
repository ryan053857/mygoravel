# 功能开关

- [简介](#introduction)
- [安装](#installation)
- [声明开关](#defining-flags)
- [检查开关](#checking-flags)
    - [在页面中检查多个开关](#checking-in-pages)
    - [后台任务](#background-jobs)
- [修改设置](#changing-settings)
    - [判断顺序](#evaluation-order)
    - [后台页面](#admin-pages)
- [删除开关](#removing-flags)
- [测试](#testing)

<a id="introduction"></a>
## 简介

功能开关让新功能先在一小部分人中上线：开发时只给员工开启，然后开放给几个试用的租户，再按比例放量，最后全量上线。出了问题可以立即全部关闭，不用重新部署。

`feature` 包分两部分：

- **开关在代码中声明**，带有一个默认规则，例如“只给员工开启”；
- **设置保存在数据库中**，由运营人员修改：全员开启、关闭或按比例放量，单独开启或关闭某个租户、某个用户。

检查开关时只读不写，也不缓存：修改设置后，所有实例的下一次检查立即生效。按比例放量用开关名和用户（或租户）ID 的哈希决定，同一个人每次的结果相同，把比例从 10% 提高到 50% 时，原来的 10% 仍然开启。

本章为博客应用上线一个新版文章编辑器。

<a id="installation"></a>
## 安装

把 `feature` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`：

```go
migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "feature", FS: feature.Migrations()},
)
```

迁移创建 `feature_settings` 表，保存全部设置。

<a id="defining-flags"></a>
## 声明开关

开关和使用它的业务放在一起。下面是 `internal/article/features.go`：

```go
package article

var NewEditor = feature.Flag{
    Name:        "articles.new-editor",
    Description: "新版文章编辑器",
    ByTenant:    true,
    Default: func(ctx context.Context, sub identity.Subject) bool {
        return sub.Can("platform:staff")
    },
}
```

| 字段 | 说明 |
| --- | --- |
| `Name` | 保存在设置中的名称，不能包含空白，发布后不要再改 |
| `Description` | 在后台页面中显示 |
| `ByTenant` | 按比例放量时按租户计算，同一租户的人看到的结果相同；为 `false` 时按用户计算 |
| `Default` | 没有任何设置命中时的结果。上面只给有 `platform:staff` 权限的员工开启；为 `nil` 时关闭 |

`Default` 可能在每次检查时调用，要足够快。它可以读取主体的权限，也可以按租户的套餐等条件判断。

在[入口](dependencies.md#assembling-the-application)的 `open` 中用全部开关创建 `Store`，保存在 `App` 中：

```go
features := feature.NewStore(db, feature.Catalog{
    article.NewEditor,
})
```

目录中的开关名重复或不合法时 `NewStore` 会 panic，启动时就能发现。

<a id="checking-flags"></a>
## 检查开关

在 action 中用 `Active` 检查，它返回 `bool`：

```go
func (a *Edit) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID) (*EditView, error) {
    if !a.features.Active(ctx, subject, NewEditor) {
        return nil, ErrNotFound.New() // 没有开启时，当作这个功能不存在
    }
    // ...
}
```

每次 `Active` 是一次带索引的查询。读取设置出错时，它记录一条 Error 日志并返回 `Default` 的结果，不会让请求失败。

<a id="checking-in-pages"></a>
### 在页面中检查多个开关

一个页面往往要检查多个开关，例如编辑页上的多个按钮。`For` 用一次查询读出这个主体的全部设置，再把结果传给 templ 组件：

```go
flags := h.features.For(r.Context(), subject)
return web.Render(w, r, http.StatusOK, views.Edit(item, views.EditOptions{
    NewEditor: flags.Active(article.NewEditor),
}))
```

`flags` 只在本次请求中使用。

<a id="background-jobs"></a>
### 后台任务

[队列任务](tenancy.md#tenant-jobs)使用 `system(tenant)` 这样的服务主体。服务主体没有用户 ID，所以租户的设置和按租户放量对它有效，用户的设置和按用户放量不适用。

<a id="changing-settings"></a>
## 修改设置

`Set` 修改一条设置，`Clear` 删除它：

```go
// 1. 给两个试用的租户开启
store.Set(ctx, article.NewEditor, feature.Tenant("acme"), feature.On)
store.Set(ctx, article.NewEditor, feature.Tenant("globex"), feature.On)

// 2. 某个用户反馈问题，单独给他关闭
store.Set(ctx, article.NewEditor, feature.User("u_42"), feature.Off)

// 3. 按比例放量
store.Set(ctx, article.NewEditor, feature.Everyone, feature.Percent(10))
store.Set(ctx, article.NewEditor, feature.Everyone, feature.Percent(50))

// 4. 发现严重问题，全部关闭
store.Set(ctx, article.NewEditor, feature.Everyone, feature.Off)

// 5. 修复后恢复放量，最终全量上线
store.Set(ctx, article.NewEditor, feature.Everyone, feature.On)
```

比例只能设给全员。开关不在目录中、租户或用户 ID 为空、比例不在 0 到 100 之间时，`Set` 返回 `feature.ErrInvalid`（`errorx.InvalidArgument`）。

修改设置的人和时间可以记录到[操作日志](activity.md)中。`WithDB` 让设置和日志在同一个事务中提交：

```go
return database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    if err := a.features.WithDB(tx).Set(ctx, flag, scope, state); err != nil {
        return err
    }
    return activity.Record(ctx, tx, subject, activity.Activity{
        Event: "feature.changed", TargetType: "feature", TargetID: flag.Name,
    })
})
```

<a id="evaluation-order"></a>
### 判断顺序

检查时从上到下，第一条命中的规则决定结果：

| 顺序 | 规则 | 结果 |
| --- | --- | --- |
| 1 | 全员设为 `On` 或 `Off` | 开启或关闭，**忽略**租户和用户的单独设置 |
| 2 | 用户的单独设置 | 开启或关闭 |
| 3 | 租户的单独设置 | 开启或关闭 |
| 4 | 全员设为 `Percent(n)` | 哈希落在 n% 内时开启 |
| 5 | 都没有 | `Default` 的结果 |

- 全员的 `On` 和 `Off` 优先于一切，所以 `Off` 能真正地紧急关闭：之前单独开启过的租户也会关闭。清除这条设置后，单独设置重新生效。
- `Percent(0)` 和 `Off` 不同：`Percent(0)` 时单独开启的租户仍然开启，`Default` 则不再使用。
- 没有用户 ID 的主体（例如未登录的访客）不在 100 以下的比例中；`ByTenant` 的开关同理看租户 ID。
- 用户的设置在他所属的每个租户中都生效。

<a id="admin-pages"></a>
### 后台页面

框架不提供管理页面。`Catalog` 列出全部开关，`Settings` 列出一个开关的全部设置（全员在前，然后是各租户、各用户），用它们在[运营后台](tenancy.md#platform)中做一个页面：

```go
for _, flag := range store.Catalog() {
    settings, err := store.Settings(ctx, flag)
    if err != nil {
        return err
    }
    for _, s := range settings {
        // s.Scope.Tenant()、s.Scope.User() 都为空时是全员；
        // s.State.Mode 为 on、off 或 percent，比例在 s.State.Percent 中
    }
}
```

修改设置的路由用权限保护，例如只允许有 `platform:features` 权限的运营人员访问。想让租户管理员自己开启某些试用功能时，可以提供一个只能修改本租户设置（`feature.Tenant(sub.Tenant)`）的页面。

<a id="removing-flags"></a>
## 删除开关

功能全量上线一段时间后，删掉代码中的判断和开关声明。新版本部署之后，用 `Prune` 删除数据库中残留的设置，它删除不在目录中的开关的全部设置，可以写成一个[命令](commands.md#custom-commands)在发布后执行：

```go
n, err := store.Prune(ctx)
```

新旧版本同时运行时（例如[不停机升级](deployment.md#zero-downtime-upgrades)），等旧版本退出后再执行，否则旧版本中的开关会暂时退回 `Default`。

<a id="testing"></a>
## 测试

在[测试数据库](testing.md#database-tests)上使用真实的 `Store`，需要开启时调用 `Set`：

```go
func TestEditWithNewEditor(t *testing.T) {
    db := newTestDB(t)
    features := feature.NewStore(db, feature.Catalog{article.NewEditor})
    require.NoError(t, features.Set(ctx, article.NewEditor, feature.Tenant("acme"), feature.On))

    edit := article.NewEdit(db, features)
    // ...
}
```
