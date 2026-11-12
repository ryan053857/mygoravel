# 验证

- [简介](#introduction)
- [快速上手](#validation-quickstart)
- [可用的验证规则](#available-validation-rules)
- [必填与可选字段](#required-and-optional-fields)
- [验证错误响应](#validation-error-response)
    - [验证提示](#validation-messages)
- [业务规则验证](#business-validation)

<a id="introduction"></a>
## 简介

验证客户端传入的数据，是每个 API 都要面对的工作。在 mygoravel 中，验证规则直接写在[输入类型](requests.md)的结构体标签上。Huma 会在调用处理器**之前**完成验证，验证失败的请求根本不会到达你的处理器。

这种方式还有一个额外的好处：验证规则会自动出现在 OpenAPI 文档中，前端开发者看到的约束与服务端实际执行的约束完全一致。

<a id="validation-quickstart"></a>
## 快速上手

假设我们要实现一个创建文章的接口：标题必填，长度不超过 200 个字符；slug 只能包含小写字母、数字和连字符。规则写在文章模块提交的数据 `Changes` 上，创建文章的输入类型把它作为请求体：

```go
type Changes struct {
    Title   string `json:"title" minLength:"1" maxLength:"200"`
    Slug    string `json:"slug" maxLength:"100" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" patternDescription:"小写字母、数字和连字符"`
    Content string `json:"content"`
}

type CreateInput struct {
    Body Changes
}
```

就是这样！当客户端发送的标题为空、slug 含有空格或大写字母时，Huma 会自动返回一个 `422` 错误响应，你的处理器不会被调用。修改文章的接口和[页面表单](forms.md)使用同一个 `Changes`，规则只写一次。

<a id="available-validation-rules"></a>
## 可用的验证规则

下面列出了最常用的验证标签，它们都来自 JSON Schema 规范：

| 标签 | 适用类型 | 说明 |
| --- | --- | --- |
| `minLength`、`maxLength` | 字符串 | 最小、最大长度 |
| `pattern` | 字符串 | 必须匹配的正则表达式 |
| `format` | 字符串 | 预定义格式，例如 `email`、`uri`、`uuid`、`date-time`、`ipv4` |
| `enum` | 字符串、数字 | 允许的取值，用逗号分隔，例如 `enum:"draft,published"` |
| `minimum`、`maximum` | 数字 | 最小值、最大值（包含边界） |
| `exclusiveMinimum`、`exclusiveMaximum` | 数字 | 最小值、最大值（不包含边界） |
| `multipleOf` | 数字 | 必须是指定数字的倍数 |
| `minItems`、`maxItems` | 数组 | 最少、最多元素个数 |
| `uniqueItems` | 数组 | 元素不能重复 |
| `default` | 任意 | 客户端省略时使用的默认值 |
| `required` | 任意 | 显式声明是否必填 |

此外，`doc` 和 `example` 标签可以为字段添加说明和示例，它们只影响文档，不参与验证。完整的标签列表请参阅 [Huma 文档](https://huma.rocks/features/request-validation/)。

<a id="required-and-optional-fields"></a>
## 必填与可选字段

不同来源的字段，默认的必填规则不同：

- **请求体字段**默认必填。在 `json` 标签中加上 `omitempty`，或者添加 `required:"false"`，字段就变为可选；
- **查询参数和请求头**默认可选，添加 `required:"true"` 可变为必填；
- **路径参数**总是必填。

```go
type PatchInput struct {
    ID   uuid.UUID `path:"id"`
    Body struct {
        Title   string  `json:"title" minLength:"1" maxLength:"200"`
        Excerpt *string `json:"excerpt,omitempty" maxLength:"500"`
    }
}
```

这里摘要 `Excerpt` 使用了指针类型，这样处理器就能区分“客户端没有传这个字段”（`nil`）和“客户端传了空字符串”（指向 `""` 的指针）。

<a id="validation-error-response"></a>
## 验证错误响应

请求体不是合法的 JSON 时返回 `400`，字段验证失败时返回 `422`。两种情况都带有 `errors` 数组，逐条说明每个字段的问题。例如，客户端创建文章时发送了空标题和不合法的 slug，请求语言是中文：

```json
{"title": "", "slug": "Hello World", "content": "……"}
```

会收到：

```json
{
  "title": "Unprocessable Entity",
  "status": 422,
  "detail": "提交的内容有误",
  "errors": [
    {"message": "必须是小写字母、数字和连字符", "location": "body.slug", "value": "Hello World", "code": "validation.pattern_description"},
    {"message": "不能为空", "location": "body.title", "value": "", "code": "validation.filled"}
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `message` | 展示给用户的提示，按请求的语言翻译 |
| `location` | 出错的位置，例如 `body.title`、`query.limit`。缺少必填字段时是这个字段的位置 |
| `value` | 服务端收到的值 |
| `code` | 违反的规则，见[验证提示](#validation-messages)，不随语言变化 |

客户端根据 `location` 把 `message` 显示在对应的表单字段旁边，需要自己的文案时按 `code` 处理。

验证错误本身不带 `code` 字段，`code` 只出现在 `errors` 的每一项中，见[错误响应格式](errors.md#response-format)。

如果路由需要认证，认证会先于验证执行。因此，一个没有携带凭据且请求体也不合法的请求，会得到 `401` 而不是 `422`。

<a id="validation-messages"></a>
### 验证提示

提示按请求协商的语言翻译，框架内置了中文和英文。`code` 就是翻译用的消息 ID：

| `code` | 规则 | 中文提示 |
| --- | --- | --- |
| `validation.required` | 缺少必填字段或参数 | 必填 |
| `validation.filled` | `minLength:"1"` | 不能为空 |
| `validation.min_length`、`validation.max_length` | `minLength`、`maxLength` | 至少 N 个字符、最多 N 个字符（按字符计数） |
| `validation.minimum`、`validation.maximum` | `minimum`、`maximum` | 不能小于 N、不能大于 N |
| `validation.pattern_description` | `pattern` 加 `patternDescription` | 必须是 + 描述，例如“必须是小写字母、数字和连字符” |
| `validation.pattern` | `pattern` | 格式不正确 |
| `validation.email`、`validation.url` | `format:"email"`、`format:"uri"` | 邮箱格式不正确、网址格式不正确 |
| `validation.one_of` | `enum` | 不是可选的值 |
| `validation.integer`、`validation.number` | 数字字段收到了其他值 | 必须是整数、必须是数字 |
| `validation.unexpected` | 请求体中有未声明的字段 | 不支持这个字段 |

完整的列表见框架的 `i18n/locales/zh-CN.yaml`，以 `validation.` 开头。在应用的翻译文件中写同名消息即可覆盖，见[本地化](localization.md)。[页面表单](forms.md#validation-messages)的提示与 API 相同。

少数无法识别的消息没有 `code`，`message` 保持 Huma 的英文原文，例如 `oneOf` 等组合规则。没有安装 `httpx.Locale` 中间件时，所有提示都是英文原文，`code` 照常返回。

<a id="business-validation"></a>
## 业务规则验证

结构体标签适合描述格式上的约束，例如长度、范围和格式。而“slug 不能与其他文章重复”、“已发布的文章不能修改 slug”这类需要查询数据或依赖业务状态的规则，应该放在 [action](actions.md) 的 `Handle` 中检查，并返回一个[业务错误](errors.md)：

```go
var ErrSlugLocked = errorx.Define(errorx.InvalidArgument, "article.slug_locked")

func (a *Update) Handle(ctx context.Context, subject identity.Subject, id uuid.UUID, c Changes) (*Article, error) {
    // ……读取文章
    if item.Status == StatusPublished && c.Slug != item.Slug {
        return nil, ErrSlugLocked.New()
    }
    // ……
}
```

`errorx.InvalidArgument` 分类的错误同样会被转换为 `422` 响应，但它没有 `errors` 数组，而是带有 `code` 字段 `article.slug_locked`，客户端可以据此给出准确的提示。完整的例子见[错误处理](errors.md#example)。

> [!TIP]
> `Handle` 也可能被命令行、队列或测试直接调用，绕过 HTTP 层的验证。对于必须成立的约束（例如标题不能为空），在 `Handle` 中再检查一次是一个好习惯。
