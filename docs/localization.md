# 本地化

- [简介](#introduction)
- [启用本地化](#enabling-localization)
    - [内置消息](#built-in-messages)
- [确定请求语言](#determining-the-locale)
- [定义翻译](#defining-translations)
    - [翻译文件](#translation-files)
    - [加载翻译](#loading-translations)
    - [覆盖内置消息](#overriding-built-in-messages)
- [获取翻译](#retrieving-translations)
- [翻译错误消息](#translating-errors)

<a id="introduction"></a>
## 简介

mygoravel 的本地化功能可以让同一个 API 根据请求的语言返回不同语言的提示文案，最常见的用途就是翻译错误响应中的 `detail` 字段。

在开始之前，有一点需要明确：本地化只影响**展示文案**。错误码、错误分类等业务语义永远保持不变，客户端应当始终根据 `code` 判断错误，而把 `detail` 直接展示给用户。

mygoravel 的本地化基于 [go-i18n](https://github.com/nicksnyder/go-i18n)。`i18n` 包负责把框架内置的消息和你的翻译文件组合成一个原生的 go-i18n `Bundle`，并在请求的 context 中携带协商出的语言。

<a id="enabling-localization"></a>
## 启用本地化

本地化默认就是开启的。`httpx.Defaults` 的第三个参数是语言目录，传 `nil` 时使用框架内置的消息，默认语言为 `zh-CN`：

```go
router := chi.NewRouter()
router.Use(httpx.Defaults(cfg.HTTP, nil, nil)...)
api := rest.New(rest.Options{Router: router})
```

这样，框架内置的错误码已经会按请求语言翻译。需要自己的翻译或其他默认语言时，用 `i18n.New` 创建语言目录再传进去，见[加载翻译](#loading-translations)。

<a id="built-in-messages"></a>
### 内置消息

框架内置了英文（`en`）和简体中文（`zh-CN`）消息，覆盖框架自身定义的业务错误码：

| 消息 ID | en | zh-CN |
| --- | --- | --- |
| `auth.unauthenticated` | Authentication required | 请先认证 |
| `auth.forbidden` | Permission denied | 没有访问权限 |
| `page.invalid_cursor` | Invalid pagination cursor | 分页游标无效 |

<a id="determining-the-locale"></a>
## 确定请求语言

`httpx.Defaults` 中的 `httpx.Locale` 中间件会根据请求的 `Accept-Language` 头，从语言目录已有的语言中选择最匹配的一种，并通过 `Content-Language` 响应头告知客户端。不支持或无法解析的语言会回退到默认语言。

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

用 `embed` 把翻译文件嵌入二进制，传给 `i18n.New`，再把得到的语言目录交给 `httpx.Defaults`：

```go
//go:embed locales/*.yaml
var localeFiles embed.FS

func NewAPI(cfg httpx.Config) (*rest.API, error) {
    languages, err := i18n.New("en", localeFiles)
    if err != nil {
        return nil, err
    }
    router := chi.NewRouter()
    router.Use(httpx.Defaults(cfg, nil, languages)...)
    return rest.New(rest.Options{Router: router}), nil
}
```

`i18n.New` 的第一个参数是默认语言，之后可以传入任意多个文件系统，框架会读取其中所有的 YAML 翻译文件。内置消息总是最先加载，不需要你手动添加。

> [!NOTE]
> `i18n.New` 返回的是原生的 go-i18n `Bundle`，它本身是可修改的。请在启动阶段完成所有修改，开始处理请求后只读取它。

<a id="overriding-built-in-messages"></a>
### 覆盖内置消息

后加载的同名消息会覆盖先加载的。由于内置消息最先加载，如果你不喜欢内置的“请先认证”，只需在应用的 `zh-CN.yaml` 中重新定义 `auth.unauthenticated`：

```yaml
auth.unauthenticated: 请先登录
```

传入多个文件系统时，规则相同：排在后面的覆盖排在前面的。

<a id="retrieving-translations"></a>
## 获取翻译

在业务代码中需要翻译其他文案时，使用 `i18n.T`，它按请求 context 中的语言翻译：

```go
message := i18n.T(ctx, "article.slug_locked", nil)

message = i18n.T(ctx, "article.slug_taken", map[string]any{"slug": slug})
```

第三个参数是模板数据，不需要时传 `nil`。请求语言中缺少这条消息时，`i18n.T` 返回默认语言的译文；默认语言也没有，或者 context 中没有语言（例如不经过 HTTP 的后台任务）时，返回消息 ID 本身。

复数形式等高级功能，通过 `i18n.Localizer(ctx)` 取得当前请求的 go-i18n `Localizer` 直接使用，请参阅 [go-i18n 文档](https://github.com/nicksnyder/go-i18n)：

```go
import goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

message, err := i18n.Localizer(ctx).Localize(&goi18n.LocalizeConfig{
    MessageID:   "article.comments",
    PluralCount: count,
})
```

`goi18n` 是 go-i18n 包的别名，用来与框架的 `i18n` 包区分。context 中没有语言时，`i18n.Localizer` 返回 `nil`。

<a id="translating-errors"></a>
## 翻译错误消息

业务错误的响应会把错误码作为消息 ID，自动翻译 `detail` 字段。你只需在翻译文件中为每个业务错误码添加一条消息即可。

如果错误通过 [`errorx.Public`](errors.md#public-parameters) 携带了公开参数，它们会作为模板数据传入翻译。例如上面的 `article.slug_taken` 翻译中使用了 `{{.slug}}`，返回错误时附加同名参数：

```go
return ErrSlugTaken.Wrap(err, errorx.Public("slug", c.Slug))
```

请求语言为中文时，得到的 `detail` 为 `slug“hello-world”已被使用`。

如果请求语言中缺少某个错误码的翻译，`detail` 会使用默认语言的译文；默认语言也没有时，使用 HTTP 状态的通用描述。这两种情况下，这个请求的[访问日志](middleware.md#access-log)都会带上 `"untranslated":true`，方便你查找需要补充的翻译。

> [!NOTE]
> 只有业务错误会被翻译。验证错误、路由不存在、服务端内部错误等不带 `code` 的错误保持 Huma 的英文描述，客户端应根据状态码自行展示提示。
