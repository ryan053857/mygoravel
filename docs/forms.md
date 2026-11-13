# 表单

- [简介](#introduction)
- [定义表单](#defining-forms)
- [渲染表单](#rendering-forms)
- [处理提交](#handling-submissions)
- [验证失败后重新显示](#redisplaying-forms)
- [业务规则验证](#business-validation)
- [验证提示](#validation-messages)

<a id="introduction"></a>
## 简介

页面中的表单和 JSON API 使用同一套[验证](validation.md)规则：在结构体字段上写 Huma 的标签。`web.ParseForm` 解析浏览器提交的表单，按标签验证，并保留用户的原始输入，验证失败时可以原样回填，作用类似 Laravel 的 `old()`。

本章接着[视图](views.md)一章，在文章页底部加上订阅新文章的表单。

<a id="defining-forms"></a>
## 定义表单

表单结构体与 API 的请求体写法相同，字段名取 `json` 标签，也就是 HTML 中 `<input>` 的 `name`。订阅表单直接使用订阅模块的 `SubscribeInput`，它也是订阅接口的请求体和 `Subscribe` action 的参数，验证规则只写一次，见[数据对象](actions.md#data-objects)：

```go
type SubscribeInput struct {
    Email  string   `json:"email" format:"email" maxLength:"254"`
    Topics []string `json:"topics,omitempty" enum:"go,database,frontend"`
    Weekly bool     `json:"weekly,omitempty"`
}
```

表单只有一层字段，类型可以是字符串、数字、布尔和它们的切片。浏览器提交的都是文本，`ParseForm` 按字段类型转换：

| 字段类型 | 转换规则 |
| --- | --- |
| 数字 | 去掉首尾空格后解析；空字符串视为未填写，无法解析时报类型错误 |
| 布尔 | `on`、`true`、`1` 为真（不区分大小写）；没有提交时为假，也就是未勾选的复选框 |
| 切片 | 同名字段的全部值，例如多选框 |

与 API 一样，没有 `omitempty` 的字段都是必填的。请求中多余的字段被忽略。转换和验证由 `form.Decode` 完成，[表格导入](import-export.md)的每一行也用它，规则完全相同。

<a id="rendering-forms"></a>
## 渲染表单

组件接收 `*web.Form[subscriber.SubscribeInput]`，交给 `web/ui` 的表单字段：每个字段带标签、说明和错误提示，回填用户的输入，有错误时标记为无效。

```templ
templ SubscribeForm(f *web.Form[subscriber.SubscribeInput]) {
	<form class="flex flex-col gap-4" hx-post="/subscribe" hx-swap="outerMorph">
		@ui.TextField(f, "email", "邮箱", ui.FieldProps{Type: "email", Attributes: templ.Attributes{"autocomplete": "email"}})
		@ui.CheckboxGroupField(f, "topics", "感兴趣的主题", topicOptions)
		@ui.SwitchField(f, "weekly", "每周汇总", ui.FieldProps{Description: "每周一封，不再逐篇通知"})
		@button.Button(button.Props{Type: button.TypeSubmit}) {
			订阅
		}
	</form>
}
```

第二个参数是字段名，即 `json` 标签；第三个参数是标签文字。按字段类型选择组件：

| 组件 | 字段类型 | 说明 |
| --- | --- | --- |
| `ui.TextField` | 字符串、数字 | `FieldProps.Type` 是 input 的类型，例如 `email`、`number`、`date`；`password` 不回填输入 |
| `ui.TextareaField` | 字符串 | `Rows` 是行数 |
| `ui.SelectField` | 字符串 | 下拉选择，选项是 `[]ui.Option`；`Placeholder` 非空时第一项的值为空 |
| `ui.RadioGroupField` | 字符串 | 一组单选项 |
| `ui.CheckboxField`、`ui.SwitchField` | 布尔 | 复选框、开关 |
| `ui.CheckboxGroupField` | 字符串切片 | 一组复选框，选中的每一项提交一个同名的值 |

`FieldProps` 还有 `ID`、`Placeholder`、`Required`、`Disabled`，`Attributes` 加在输入控件上，例如 `autocomplete`。需要别的布局时，直接组合[界面组件](components.md#using-components)，用 `f.Value`、`f.Values`、`f.Checked` 回填，`f.Error` 取错误提示。

文章页第一次显示表单时，在[渲染页面](views.md#rendering-pages)的 `Show` 中用 `web.NewForm` 包装初始数据，例如默认勾选当前文章的主题，`Value` 返回各字段的文本：

```go
f := web.NewForm(subscriber.SubscribeInput{Topics: []string{item.Topic}})
return web.Render(w, r, http.StatusOK, views.Article(item, f))
```

<a id="handling-submissions"></a>
## 处理提交

`ParseForm` 解析请求体并验证，`Valid` 报告是否通过，解码后的数据在 `Data` 中：

```go
func (h *Handlers) Subscribe(w http.ResponseWriter, r *http.Request) error {
    f, err := web.ParseForm[subscriber.SubscribeInput](r)
    if err != nil {
        return err
    }
    if !f.Valid() {
        return web.Render(w, r, http.StatusUnprocessableEntity, views.SubscribeForm(f))
    }
    if err := h.subscribe.Handle(r.Context(), f.Data); err != nil {
        return err
    }
    return web.Render(w, r, http.StatusOK, views.Subscribed(f.Data.Email))
}
```

只有请求体无法解析时，`ParseForm` 才返回错误（`web.ErrBadForm`，响应 `422`）。订阅成功后，`Subscribed` 片段替换表单，显示「确认邮件已发到 …」。需要整页跳转时用 `web.Redirect`，见[跳转](views.md#redirects)。

公开的表单要防止被脚本反复提交：用[限流](rate-limiting.md)按 IP 限制这个路由，必要时加上[人机验证](login.md#captcha)。

<a id="redisplaying-forms"></a>
## 验证失败后重新显示

验证失败时，处理器返回 `422` 和重新渲染的表单。htmx 4 默认会替换 4xx 响应，表单上的 `hx-swap="outerMorph"` 用新表单替换旧表单：

- `Value` 返回用户提交的原始文本。即使在数字字段里填了 `abc`，回填的也是 `abc`，而不是转换失败后的零值；
- morph 只更新变化的部分，输入框的焦点和 Alpine 的状态都会保留，见[前端状态](views.md#client-state)。

<a id="business-validation"></a>
## 业务规则验证

需要查询数据的规则在 action 中检查，并返回[业务错误](validation.md#business-validation)，例如这个邮箱已经订阅过。把它放到对应字段旁边时，用 `SetError`：

```go
if err := h.subscribe.Handle(r.Context(), f.Data); errors.Is(err, subscriber.ErrAlreadySubscribed) {
    f.SetError("email", httpx.ErrorDetail(r.Context(), err))
    return web.Render(w, r, http.StatusUnprocessableEntity, views.SubscribeForm(f))
} else if err != nil {
    return err
}
```

`httpx.ErrorDetail` 按错误码翻译业务错误，与 JSON API 响应中的 `detail` 相同。没有对应字段的业务错误直接返回即可，`pages` 会把它显示为错误提示，见[错误页](views.md#error-pages)。

<a id="validation-messages"></a>
## 验证提示

验证提示按请求协商的语言翻译，框架内置了中文和英文，例如 `minLength:"1"` 的字段为空时提示“不能为空”，`pattern` 加 `patternDescription` 时提示“必须是”加描述。JSON API 的验证错误使用同一套提示，完整的规则和覆盖方法见[验证提示](validation.md#validation-messages)。没有安装 `httpx.Locale` 中间件时，提示是 Huma 的英文原文。
