# 本地化

- [简介](#introduction)
- [启用本地化](#enabling-localization)
    - [内置消息](#built-in-messages)
- [确定请求语言](#determining-the-locale)
- [定义翻译](#defining-translations)
    - [翻译文件](#translation-files)
    - [加载翻译](#loading-translations)
    - [覆盖优先级](#override-priority)
- [获取翻译](#retrieving-translations)
- [翻译错误消息](#translating-errors)

<a id="introduction"></a>
## 简介

mygoravel 的本地化功能可以让同一个 API 根据请求的语言返回不同语言的提示文案，最常见的用途就是翻译错误响应中的 `detail` 字段。

在开始之前，有一点需要明确：本地化只影响**展示文案**。错误码、错误分类等业务语义永远保持不变，客户端应当始终根据 `code` 判断错误，而把 `detail` 直接展示给用户。

mygoravel 的本地化基于 [go-i18n](https://github.com/nicksnyder/go-i18n)。`i18n` 包负责把框架内置的消息和你的翻译文件组合成一个原生的 go-i18n `Bundle`，之后你可以直接使用 go-i18n 的全部能力。

<a id="enabling-localization"></a>
## 启用本地化

下面是一个完整的函数，它创建包含内置消息的语言目录，并把它传给 API：

```go
package api

import (
    "github.com/go-chi/chi/v5"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/i18n"
)

func NewAPI(cfg httpx.Config) (*rest.API, error) {
    bundle, err := i18n.New(i18n.Options{
        Default: "zh-CN",
        Layers:  []i18n.Layer{i18n.FrameworkLayer()},
    })
    if err != nil {
        return nil, err
    }
    router := chi.NewRouter()
    router.Use(httpx.Defaults(cfg, httpx.DefaultsOptions{Languages: bundle})...)
    return rest.New(rest.Options{Router: router, Languages: bundle}), nil
}
```

语言目录要同时交给两处：`httpx.Defaults` 用它协商请求语言，`rest.New` 用它翻译错误消息。

`Default` 是默认语言，当请求的语言不受支持时使用它。省略时默认为 `zh-CN`，你也可以设置为 `en`。

<a id="built-in-messages"></a>
### 内置消息

`i18n.FrameworkLayer()` 提供了框架内置的英文（`en`）和简体中文（`zh-CN`）消息，覆盖框架自身定义的业务错误码：

| 消息 ID | en | zh-CN |
| --- | --- | --- |
| `auth.unauthenticated` | Authentication required | 请先认证 |
| `auth.forbidden` | Permission denied | 没有访问权限 |
| `page.invalid_cursor` | Invalid pagination cursor | 分页游标无效 |

<a id="determining-the-locale"></a>
## 确定请求语言

启用本地化后，`httpx.Locale` 中间件会根据请求的 `Accept-Language` 头，从已加载的语言中选择最匹配的一种，并通过 `Content-Language` 响应头告知客户端。不支持或无法解析的语言会回退到默认语言。

```bash
curl -i http://localhost:8080/articles -H 'Accept-Language: en'
```

假设 `/articles` 是一个需要认证的路由，这个请求由于没有携带凭据会返回 `401`，`detail` 为英文的 `Authentication required`，而 `code` 始终是 `auth.unauthenticated`。

在代码中，可以用 `i18n.Locale(ctx)` 读取当前请求选定的语言。

<a id="defining-translations"></a>
## 定义翻译

<a id="translation-files"></a>
### 翻译文件

为应用自己的消息创建翻译文件，文件名就是语言标签。例如 `locales/en.yaml`：

```yaml
article.not_found: Article not found
article.slug_locked: The slug of a published article cannot be changed
article.slug_taken: "The slug \"{{.slug}}\" is already taken"
```

以及 `locales/zh-CN.yaml`：

```yaml
article.not_found: 文章不存在
article.slug_locked: 已发布文章的 slug 不能修改
article.slug_taken: "slug“{{.slug}}”已被使用"
```

消息 ID 与[业务错误码](errors.md#defining-errors)使用相同的写法，这样错误响应就能直接找到对应的翻译。

<a id="loading-translations"></a>
### 加载翻译

用 `embed` 把翻译文件嵌入二进制，作为一个新的消息层传给 `i18n.New`：

```go
//go:embed locales/*.yaml
var localeFiles embed.FS

func newBundle() (*goi18n.Bundle, error) {
    return i18n.New(i18n.Options{
        Default: "en",
        Layers: []i18n.Layer{
            i18n.FrameworkLayer(),
            {Priority: i18n.PriorityProduct, FS: localeFiles, Name: "blog"},
        },
        RequiredIDs: []string{"article.not_found", "article.slug_locked", "article.slug_taken"},
    })
}
```

这里的 `goi18n` 是 `github.com/nicksnyder/go-i18n/v2/i18n` 的别名，用来与框架的 `i18n` 包区分。每一层都必须提供 `FS`，框架会读取其中所有的 YAML 翻译文件。`Name` 只用于错误信息，方便定位是哪一层出了问题。

`RequiredIDs` 列出默认语言中**必须存在**的消息。如果漏写了某条翻译，`i18n.New` 会直接返回错误，问题在启动时就能被发现。

> [!NOTE]
> `i18n.New` 返回的是原生的 go-i18n `Bundle`，它本身是可修改的。请在启动阶段完成所有修改，开始处理请求后只读取它。

<a id="override-priority"></a>
### 覆盖优先级

每一层消息都有一个优先级，高优先级的层可以覆盖低优先级层中的同名消息：

| 优先级 | 常量 | 用途 |
| --- | --- | --- |
| 0 | `i18n.PriorityFramework` | 框架内置消息 |
| 1 | `i18n.PriorityModule` | 可复用模块提供的消息 |
| 2 | `i18n.PriorityProduct` | 应用自己的消息 |

例如，如果你不喜欢内置的“请先认证”，只需在应用的 `zh-CN.yaml` 中重新定义 `auth.unauthenticated`。同一优先级、同一语言中出现重复的消息 ID 会返回错误。

<a id="retrieving-translations"></a>
## 获取翻译

在业务代码中需要翻译其他文案时，直接使用 go-i18n 的 `Localizer`，并通过 `i18n.Locale(ctx)` 获取当前请求的语言：

```go
localizer := goi18n.NewLocalizer(bundle, i18n.Locale(ctx))
message, err := localizer.Localize(&goi18n.LocalizeConfig{
    MessageID: "article.slug_locked",
})
```

模板变量、复数形式等高级功能都通过 go-i18n 原生的 `LocalizeConfig` 使用，请参阅 [go-i18n 文档](https://github.com/nicksnyder/go-i18n)。

<a id="translating-errors"></a>
## 翻译错误消息

启用本地化后，业务错误的响应会把错误码作为消息 ID，自动翻译 `detail` 字段。你只需在翻译文件中为每个业务错误码添加一条消息即可。

如果错误通过 [`errorx.Public`](errors.md#public-parameters) 携带了公开参数，它们会作为模板数据传入翻译。例如上面的 `article.slug_taken` 翻译中使用了 `{{.slug}}`，返回错误时附加同名参数：

```go
return ErrSlugTaken.Wrap(err, errorx.Public("slug", c.Slug))
```

请求语言为中文时，得到的 `detail` 为 `slug“hello-world”已被使用`。

如果找不到某个错误码的翻译，`detail` 会使用 HTTP 状态的通用描述，同时（在配置了日志器时）记录一条缺失翻译的警告日志，提醒你补充翻译。

> [!NOTE]
> 只有业务错误会被翻译。验证错误、路由不存在、服务端内部错误等不带 `code` 的错误保持 Huma 的英文描述，客户端应根据状态码自行展示提示。
