# 设置

- [简介](#introduction)
- [安装](#installation)
- [声明设置](#defining-settings)
- [读取设置](#reading-settings)
- [修改设置](#changing-settings)
    - [并发修改](#concurrent-updates)
    - [记录操作日志](#logging-changes)
- [租户的设置](#tenant-settings)
- [修改字段](#changing-fields)
- [后台页面与接口](#admin-pages)
- [测试](#testing)

<a id="introduction"></a>
## 简介

[配置](configuration.md)在部署时决定，改了要重启；设置由管理员在运行时修改，例如站点名称、是否开放注册、每个租户的发票抬头。

`settings` 包把一组设置声明成一个结构体，默认值写在代码中，整组保存为 `settings` 表中的一行 JSON。读取时把保存的 JSON 合并到默认值上：

- 新增字段不需要迁移，读取时使用默认值；
- 删除的字段被忽略，下次保存时去掉；
- 只有字段改名或改类型时，才需要写一条迁移转换保存的 JSON。

读取时不缓存，每次 `Get` 是一次主键查询，修改后所有实例立即生效。

<a id="installation"></a>
## 安装

把 `settings` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`：

```go
migratecmd.Publish(
    migrate.Source{Name: "settings", FS: settings.Migrations()},
)
```

<a id="defining-settings"></a>
## 声明设置

设置和使用它的业务放在一起。下面是 `internal/site/settings.go`：

```go
package site

type Settings struct {
    Name          string   `json:"name" minLength:"1" maxLength:"60"`
    SignupOpen    bool     `json:"signup_open"`
    ModerateFirst bool     `json:"moderate_first" doc:"新用户的第一条评论先审核"`
    BannedWords   []string `json:"banned_words" maxItems:"200"`
}

var Defaults = Settings{
    Name:       "My Blog",
    SignupOpen: true,
}
```

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建 `Group`，保存在 `App` 中，再传给需要它的 action：

```go
siteSettings := settings.New(db, "site", site.Defaults)
```

名称 `"site"` 保存在表中，发布后不要再改。以下情况 `New` 会 panic，启动时就能发现：名称为空或包含空白、类型不是结构体、字段带有 `omitempty` 或 `omitzero`。最后一条是因为零值不会写进 JSON，读取时又会被默认值覆盖：`SignupOpen` 默认为 `true`，关闭之后再读又变回 `true`。

<a id="reading-settings"></a>
## 读取设置

```go
func (a *Register) Handle(ctx context.Context, in RegisterInput) (*User, error) {
    s, err := a.site.Get(ctx)
    if err != nil {
        return nil, err
    }
    if !s.SignupOpen {
        return nil, ErrSignupClosed.New()
    }
    // ...
}
```

没有保存过时 `Get` 返回默认值。每次返回的是新的值，修改它不影响下一次读取。读取出错时 `Get` 返回错误，不会悄悄退回默认值：已经关闭的注册不会因为数据库抖动重新开放。

<a id="changing-settings"></a>
## 修改设置

`Save` 用整组值覆盖保存的设置，`Reset` 删除它，恢复默认值：

```go
err := a.site.Save(ctx, s)
err := a.site.Reset(ctx)
```

<a id="concurrent-updates"></a>
### 并发修改

`Save` 是后写覆盖先写：两个管理员同时打开同一个设置页，后提交的那个覆盖前一个，这与表单提交整组值的习惯一致。只修改部分字段时用 `Update`，它在事务中锁住这一行，读出、修改、保存，并发的 `Update` 依次执行：

```go
err := a.site.Update(ctx, func(s *site.Settings) error {
    s.BannedWords = append(s.BannedWords, word)
    return nil
})
```

`fn` 返回错误时不保存，`Update` 原样返回这个错误。

<a id="logging-changes"></a>
### 记录操作日志

`WithDB` 返回使用事务的副本，让设置和[操作日志](activity.md)一起提交：

```go
return database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    if err := a.site.WithDB(tx).Save(ctx, s); err != nil {
        return err
    }
    return activity.Record(ctx, tx, subject, activity.Activity{
        Event: "settings.changed", TargetType: "settings", TargetID: "site",
    })
})
```

<a id="tenant-settings"></a>
## 租户的设置

同一组设置可以为每个[租户](tenancy.md)各保存一份，`Tenant` 返回读写某个租户的副本：

```go
s, err := a.blog.Tenant(subject.Tenant).Get(ctx)
```

租户的设置和全局设置互不影响：租户没有保存过时使用代码中的默认值，**不会**回落到全局设置。租户 ID 为空时每个方法都返回 `settings.ErrInvalid`，不会误读成全局设置，所以未登录的请求要先确定租户再读取。

<a id="changing-fields"></a>
## 修改字段

| 修改 | 需要做什么 |
| --- | --- |
| 新增字段 | 在 `Defaults` 中给出默认值。已保存的设置读取时使用它 |
| 删除字段 | 什么也不用做。旧字段留在 JSON 中，下次保存时去掉 |
| 新增嵌套结构体的字段 | 什么也不用做。嵌套的结构体逐个字段合并 |
| 改名、改类型 | 写一条迁移转换保存的 JSON |

只有结构体逐个字段合并。切片、map 和指针整体使用保存的值：默认的 `BannedWords` 是 `["spam"]`、保存的是 `[]` 时，读到的是 `[]`。

改名时用一条普通的 SQL [迁移](migrations.md)：

```sql
-- +goose Up
UPDATE settings
SET payload = payload - 'moderate' || jsonb_build_object('moderate_first', payload -> 'moderate')
WHERE name = 'site' AND payload ? 'moderate';
```

改了类型又没有迁移时，`Get` 返回错误，例如 `settings: decode "site": json: cannot unmarshal number into Go struct field ...`。

一组设置不再使用时，删掉代码，再写一条迁移 `DELETE FROM settings WHERE name = 'site'`。

<a id="admin-pages"></a>
## 后台页面与接口

框架不提供设置页面。设置的结构体可以直接作为 [JSON API](routing.md) 的请求体和响应体，Huma 按字段的标签生成文档并校验。字段都没有 `omitempty`，所以请求体必须给出全部字段，正好对应 `Save` 整组保存：

```go
type SiteSettingsBody struct {
    Body site.Settings
}

func (a *SaveSettings) HTTP(ctx context.Context, in *SiteSettingsBody) (*SiteSettingsBody, error) {
    subject, _ := identity.FromContext(ctx)
    if err := a.Handle(ctx, subject, in.Body); err != nil {
        return nil, err
    }
    return &SiteSettingsBody{Body: in.Body}, nil
}
```

在[运营后台](tenancy.md#platform)的页面中，同一个结构体也可以用作[表单](forms.md)，验证规则只写一次；表单只有一层字段，带嵌套结构体的设置要拆开。修改设置的路由用权限保护，例如只允许有 `platform:settings` 权限的运营人员访问；租户管理员只能修改 `Tenant(sub.Tenant)` 的设置。

<a id="testing"></a>
## 测试

在[测试数据库](testing.md#database-tests)上使用真实的 `Group`，需要非默认值时先 `Save`：

```go
func TestRegisterWhenSignupClosed(t *testing.T) {
    db := newTestDB(t)
    siteSettings := settings.New(db, "site", site.Defaults)
    s := site.Defaults
    s.SignupOpen = false
    require.NoError(t, siteSettings.Save(ctx, s))

    register := user.NewRegister(db, siteSettings)
    // ...
}
```
