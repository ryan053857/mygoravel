# 表单

- [简介](#introduction)
- [定义表单](#defining-forms)
- [渲染表单](#rendering-forms)
- [处理提交](#handling-submissions)
- [验证失败后重新显示](#redisplaying-forms)
- [业务规则验证](#business-validation)
- [验证提示](#validation-messages)
- [上传文件](#file-uploads)

<a id="introduction"></a>
## 简介

页面中的表单和 JSON API 使用同一套[验证](validation.md)规则：在结构体字段上写 Huma 的标签。`web.ParseForm` 解析浏览器提交的表单，按标签验证，并保留用户的原始输入，验证失败时可以原样回填，作用类似 Laravel 的 `old()`。

本章接着[视图](views.md)一章，完成博客编辑页的修改表单。

<a id="defining-forms"></a>
## 定义表单

表单结构体与 API 的请求体写法相同，字段名取 `json` 标签，也就是 HTML 中 `<input>` 的 `name`。编辑文章的表单直接使用文章模块的 `Changes`，它也是修改文章接口的请求体和 `Update` action 的参数，验证规则只写一次，见[数据对象](actions.md#data-objects)：

```go
type Changes struct {
    Title   string `json:"title" minLength:"1" maxLength:"200"`
    Slug    string `json:"slug" maxLength:"100" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" patternDescription:"小写字母、数字和连字符"`
    Content string `json:"content"`
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

组件接收 `*web.Form[article.Changes]`，交给 `web/ui` 的表单字段：每个字段带标签、说明和错误提示，回填用户的输入，有错误时标记为无效。

```templ
templ ArticleForm(id uuid.UUID, f *web.Form[article.Changes]) {
	<form class="flex flex-col gap-4" hx-put={ "/admin/articles/" + id.String() } hx-swap="outerMorph">
		@ui.TextField(f, "title", "标题")
		@ui.TextField(f, "slug", "Slug", ui.FieldProps{Description: "小写字母、数字和连字符"})
		@ui.TextareaField(f, "content", "正文", ui.FieldProps{Rows: 12})
		@button.Button(button.Props{Type: button.TypeSubmit}) {
			保存
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

```templ
@ui.SelectField(f, "status", "状态", []ui.Option{{Value: "draft", Label: "草稿"}, {Value: "published", Label: "已发布"}})
@ui.CheckboxGroupField(f, "tags", "标签", tagOptions)
@ui.SwitchField(f, "pinned", "置顶", ui.FieldProps{Description: "显示在首页顶部"})
```

`FieldProps` 还有 `ID`、`Placeholder`、`Required`、`Disabled`，`Attributes` 加在输入控件上，例如 `autocomplete`。需要别的布局时，直接组合[界面组件](components.md#using-components)，用 `f.Value`、`f.Values`、`f.Checked` 回填，`f.Error` 取错误提示。

首次打开编辑页时，把[渲染页面](views.md#rendering-pages)中的 `Edit` 改为用 `web.NewForm` 包装已有数据，`Value` 返回各字段的文本：

```go
in := article.Changes{Title: item.Title, Slug: item.Slug, Content: item.Content}
return web.Render(w, r, http.StatusOK, views.EditArticle(item.ID, web.NewForm(in)))
```

<a id="handling-submissions"></a>
## 处理提交

`ParseForm` 解析请求体并验证，`Valid` 报告是否通过，解码后的数据在 `Data` 中：

```go
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) error {
    id, err := uuid.Parse(chi.URLParam(r, "id"))
    if err != nil {
        return web.ErrNotFound.New()
    }
    f, err := web.ParseForm[article.Changes](r)
    if err != nil {
        return err
    }
    if !f.Valid() {
        return web.Render(w, r, http.StatusUnprocessableEntity, views.ArticleForm(id, f))
    }
    sub, _ := identity.FromContext(r.Context())
    if _, err := h.update.Handle(r.Context(), sub, id, f.Data); err != nil {
        return err
    }
    web.Flash(r.Context(), web.Message{Type: "success", Title: "文章已保存"})
    web.Redirect(w, r, "/admin/articles")
    return nil
}
```

只有请求体无法解析时，`ParseForm` 才返回错误（`web.ErrBadForm`，响应 `422`）。保存成功后，`web.Redirect` 让浏览器跳转到列表页，见[跳转](views.md#redirects)；`web.Flash` 让列表页显示一次「文章已保存」，见[提示消息](views.md#flash-messages)。

<a id="redisplaying-forms"></a>
## 验证失败后重新显示

验证失败时，处理器返回 `422` 和重新渲染的表单。htmx 4 默认会替换 4xx 响应，表单上的 `hx-swap="outerMorph"` 用新表单替换旧表单：

- `Value` 返回用户提交的原始文本。即使在数字字段里填了 `abc`，回填的也是 `abc`，而不是转换失败后的零值；
- morph 只更新变化的部分，输入框的焦点和 Alpine 的状态都会保留，见[前端状态](views.md#client-state)。

<a id="business-validation"></a>
## 业务规则验证

需要查询数据的规则在 action 中检查，并返回[业务错误](validation.md#business-validation)，例如已发布的文章不能修改 slug。把它放到对应字段旁边时，用 `SetError`：

```go
if _, err := h.update.Handle(r.Context(), sub, id, f.Data); errors.Is(err, article.ErrSlugLocked) {
    f.SetError("slug", httpx.ErrorDetail(r.Context(), err))
    return web.Render(w, r, http.StatusUnprocessableEntity, views.ArticleForm(id, f))
} else if err != nil {
    return err
}
```

`httpx.ErrorDetail` 按错误码翻译业务错误，与 JSON API 响应中的 `detail` 相同。没有对应字段的业务错误直接返回即可，`pages` 会把它放进错误提示区域，见[错误页](views.md#error-pages)。

<a id="validation-messages"></a>
## 验证提示

验证提示按请求协商的语言翻译，框架内置了中文和英文，例如 `minLength:"1"` 的字段为空时提示“不能为空”，`pattern` 加 `patternDescription` 时提示“必须是”加描述。JSON API 的验证错误使用同一套提示，完整的规则和覆盖方法见[验证提示](validation.md#validation-messages)。没有安装 `httpx.Locale` 中间件时，提示是 Huma 的英文原文。

<a id="file-uploads"></a>
## 上传文件

文件不随表单一起提交，而是在用户选择后立即上传为[临时文件](filesystem.md#introduction)，表单只提交文件 ID；保存时业务代码确认它。这样大文件可以显示进度、直传到 S3，表单验证失败也不用重新上传。

先注册上传接口。`upload.Handler` 是一个页面处理器，上传者就是当前登录的主体：

```go
pages.Routes(r, web.Require()).Post("/admin/uploads", upload.Handler(files, upload.Options{}))
```

`files` 是[文件服务](filesystem.md#assembling-files)，`upload` 是 `github.com/ryan053857/mygoravel/web/upload`。磁盘支持直传（S3）时，浏览器拿到签名后把文件直接传到存储；本地磁盘则通过这个接口上传，请求体受 [`httpx.BodyLimit`](requests.md#body-size) 限制。`Options.Disk` 指定磁盘，为空时是默认磁盘。

给文章加上封面，表单结构体中是文件 ID：

```go
type Changes struct {
    // ……
    CoverID string `json:"cover_id,omitempty"`
}
```

表单中用 `ui.FileField`：

```templ
@ui.FileField(f, "cover_id", "封面", ui.FileFieldProps{
	Upload:  "/admin/uploads",
	Accept:  "image/*",
	MaxSize: 5 << 20,
})
```

- 选择文件后立即上传，显示进度、文件名和大小，图片显示缩略图，可以移除；上传期间表单不能提交。
- `Accept` 只用于筛选文件选择框，`MaxSize` 让超过大小的文件在浏览器中直接提示。文件的真实类型和大小由确认时的 `Accept` 检查。
- `Multiple` 允许选择多个文件，字段对应字符串切片。
- 博客的表单是 htmx 表单，验证失败重新渲染时，字段保留浏览器中已上传的文件。编辑页要显示已有的封面，或者整页表单验证失败后要显示已上传的文件时，用 `Files` 传入文件信息：`upload.Files(ctx, files, f.Values("cover_id")...)`。
- 编辑页显示已保存的图片时默认加载原图。`Preview` 把地址转换为缩略图地址，例如 `Preview: func(u string) string { return images.URL(u, imgproxy.Options{Width: 80, Height: 80, Resize: imgproxy.Fill}) }`，见[图片缩略图](filesystem.md#thumbnails)。刚选择的文件在浏览器本地预览，不经过它。

保存时，`Update` action 在事务中确认封面，见[确认文件](filesystem.md#confirming-files)。确认失败（例如不是图片）时把错误放到字段旁：

```go
if errors.Is(err, filesystem.ErrNotAccepted) {
    f.SetError("cover_id", httpx.ErrorDetail(r.Context(), err))
    return web.Render(w, r, http.StatusUnprocessableEntity, views.ArticleForm(id, f))
}
```

用户移除了文件时，表单不再提交它的 ID；更换封面时删除旧文件，见[替换文件](filesystem.md#replacing-files)。
