# 导入与导出

- [简介](#introduction)
- [导入](#importing)
    - [对应的列](#columns)
    - [编写导入 action](#import-action)
    - [下载模板](#templates)
    - [读取规则](#reading-rules)
- [导出](#exporting)
    - [编写导出 action](#export-action)
    - [单元格的值](#cell-values)
- [大文件](#large-files)

<a id="introduction"></a>
## 简介

后台管理系统几乎都要支持用 Excel 批量导入数据，以及把列表导出成表格。`sheet` 包读写 CSV 和 Excel（XLSX）两种格式，导入时按[验证](validation.md)规则检查每一行，规则与 JSON 接口和[页面表单](forms.md)用的是同一个结构体。

本章为博客加上两个功能：从表格批量创建文章，以及把文章列表导出为表格。两者都在一次请求中完成，适合几千行以内的数据。

<a id="importing"></a>
## 导入

<a id="columns"></a>
### 对应的列

导入的每一行就是一次“创建文章”，所以直接解码为 `article.Changes`。表格的表头是给人看的，通常写成中文；在字段上加 `sheet` 标签，写出这一列在表格中的名称：

```go
type Changes struct {
    Title   string `json:"title" sheet:"标题" minLength:"1" maxLength:"200"`
    Slug    string `json:"slug" sheet:"Slug" maxLength:"100" pattern:"^[a-z0-9]+(-[a-z0-9]+)*$" patternDescription:"小写字母、数字和连字符"`
    Content string `json:"content" sheet:"正文,内容"`
}
```

表头与字段的 json 标签或 sheet 标签中的任意一个名称相同，这一列就对应这个字段。比较时不区分大小写，并忽略空白、连字符和下划线，所以 `Published At`、`published_at` 都能对应 `json:"published_at"`。sheet 标签可以用逗号写多个名称，第一个是[模板](#templates)中使用的列名。不需要导入的字段写 `sheet:"-"`。

表格中多余的列被忽略。必填字段（没有 `omitempty` 的字段）找不到对应的列时，整个文件被拒绝，错误是 `sheet.ErrMissingColumns`，提示中列出缺少的列名。

<a id="import-action"></a>
### 编写导入 action

[业务动作](actions.md#direct-calls)一章提到，导入文章复用 `Create` 的规则。下面是完整的 `internal/article/import.go`：

```go
package article

import (
    "context"

    "github.com/cockroachdb/errors"
    "github.com/danielgtaylor/huma/v2"

    "github.com/ryan053857/mygoravel/i18n"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/sheet"
)

// Import 从表格批量创建文章。
type Import struct {
    create *Create
}

func NewImport(create *Create) *Import {
    return &Import{create: create}
}

// ImportResult 是导入的结果：创建了多少篇文章，以及没有导入的行。
type ImportResult struct {
    Created  int             `json:"created"`
    Failures []ImportFailure `json:"failures"`
}

// ImportFailure 是没有导入的一行，Errors 以字段名为键。
type ImportFailure struct {
    Line   int               `json:"line"`
    Errors map[string]string `json:"errors"`
}

// Handle 逐行创建文章。每一行单独提交，一行失败不影响其他行；只有文件无法读取或数据库出错时返回错误。
func (a *Import) Handle(ctx context.Context, subject identity.Subject, r *sheet.Reader) (ImportResult, error) {
    result := ImportResult{Failures: []ImportFailure{}}
    records, err := sheet.Decode[Changes](ctx, r)
    if err != nil {
        return result, err
    }
    for rec, err := range records {
        if err != nil {
            return result, err
        }
        if rec.Valid() {
            _, err := a.create.Handle(ctx, subject, rec.Data)
            if errors.Is(err, ErrSlugTaken) {
                rec.SetError("slug", i18n.T(ctx, ErrSlugTaken.Code(), nil))
            } else if err != nil {
                return result, err
            }
        }
        if !rec.Valid() {
            result.Failures = append(result.Failures, ImportFailure{Line: rec.Line, Errors: rec.Errors})
            continue
        }
        result.Created++
    }
    return result, nil
}

type ImportInput struct {
    RawBody huma.MultipartFormFiles[struct {
        File huma.FormFile `form:"file" required:"true"`
    }]
}

type ImportOutput struct {
    Body ImportResult
}

// HTTP 处理 POST /articles/import，上传 .csv 或 .xlsx 文件。
func (a *Import) HTTP(ctx context.Context, in *ImportInput) (*ImportOutput, error) {
    subject, _ := identity.FromContext(ctx)
    file := in.RawBody.Data().File
    format, err := sheet.FormatOf(file.Filename)
    if err != nil {
        return nil, err
    }
    r, err := sheet.NewReader(file, format)
    if err != nil {
        return nil, err
    }
    defer r.Close()
    result, err := a.Handle(ctx, subject, r)
    if err != nil {
        return nil, err
    }
    return &ImportOutput{Body: result}, nil
}
```

`sheet.Decode` 返回一个迭代器，每次产出一行 `sheet.Record`：`Line` 是这一行在 Excel 中的行号，`Data` 是解码后的 `Changes`，`Errors` 是验证失败的提示，与[表单的验证提示](forms.md#validation-messages)相同，按请求的语言翻译。验证失败的行同样会产出，用 `Valid` 判断。

`Create` 返回的业务错误分两种处理：slug 重复只是这一行的问题，用 `SetError` 放到 `slug` 字段上，接着处理下一行；其他错误（例如没有权限、数据库不可用）对每一行都一样，直接结束导入。前端拿到的结果类似：

```json
{
  "created": 41,
  "failures": [
    {"line": 7, "errors": {"title": "不能为空"}},
    {"line": 12, "errors": {"slug": "slug 已被占用"}}
  ]
}
```

每一行单独提交，前面导入成功的行不会因为后面的失败而回滚，用户修改失败的行后重新导入即可。如果业务要求全部成功才导入，先遍历一次只做验证，全部有效后再在一个事务中写入。

在 `routes.go` 中注册路由：

```go
importArticles := huma.Operation{
    OperationID: "article-import",
    Method:      http.MethodPost,
    Path:        "/articles/import",
    Summary:     "从表格导入文章",
}
requireArticles(&importArticles)
rest.Register(api, importArticles, NewImport(NewCreate(db)).HTTP)
```

命令行入口用 `os.Open` 打开本地文件，用 `sheet.FormatOf(path)` 取得格式，再调用同一个 `Handle`，写法见[命令行](actions.md#as-commands)。

<a id="templates"></a>
### 下载模板

给用户一个填好表头的模板，比让他们自己拼列名可靠得多。`sheet.Template` 按 sheet 标签写出表头，并把传入的值写成示例行。填好的模板可以原样导入。在 `import.go` 中加入：

```go
type TemplateInput struct {
    Format sheet.Format `query:"format" enum:"csv,xlsx" default:"xlsx"`
}

// Template 处理 GET /articles/import/template，下载导入模板。
func (a *Import) Template(ctx context.Context, in *TemplateInput) (*huma.StreamResponse, error) {
    return download("articles-template", in.Format, func(w *sheet.Writer) error {
        return sheet.Template(w, Changes{Title: "第一篇文章", Slug: "hello-world", Content: "正文"})
    }), nil
}
```

`download` 写出附件响应，见[编写导出 action](#export-action)。

<a id="reading-rules"></a>
### 读取规则

`sheet.NewReader` 读出第一个非空行作为表头，`Rows` 和 `Decode` 跳过空行，单元格的值去掉首尾空白。

- **XLSX** 读取第一个工作表。单元格的值是 Excel 中显示的文本，例如日期按单元格设置的格式显示，布尔值是 `TRUE`、`FALSE`。整个文件会被读入内存，所以上传接口要限制文件大小，用完调用 `Close`。
- **CSV** 去掉开头的 BOM；内容不是 UTF-8 时按 GB18030 解码，Excel 中文版另存的 CSV 就是这种编码。分隔符按表头一行在逗号、分号和制表符中自动选择。

转换规则与[表单](forms.md#defining-forms)相同，例如数字字段的空单元格视为未填写，布尔字段接受 `true`、`false`、`1`、`0`（不区分大小写）。切片字段的单元格按逗号拆分为多个值，中文逗号也可以。

<a id="exporting"></a>
## 导出

<a id="export-action"></a>
### 编写导出 action

导出的每一列由 `sheet.Column` 描述：`Header` 是表头，`Value` 从一行数据中取出单元格的值，`Key` 用于让用户选择导出哪些列。下面是完整的 `internal/article/export.go`：

```go
package article

import (
    "context"
    "log/slog"
    "mime"
    "time"

    "github.com/cockroachdb/errors"
    "github.com/danielgtaylor/huma/v2"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/sheet"
)

// exportColumns 是可以导出的列。
var exportColumns = sheet.Columns[Article]{
    {Key: "title", Header: "标题", Value: func(a Article) any { return a.Title }},
    {Key: "slug", Header: "Slug", Value: func(a Article) any { return a.Slug }},
    {Key: "status", Header: "状态", Value: func(a Article) any { return a.Status }},
    {Key: "published_at", Header: "发布时间", Value: func(a Article) any { return a.PublishedAt }},
}

// Export 把文章列表导出为表格。
type Export struct {
    db *bun.DB
}

func NewExport(db *bun.DB) *Export {
    return &Export{db: db}
}

// Handle 把 subject 能看到的文章按 cols 写入 w。
func (a *Export) Handle(ctx context.Context, subject identity.Subject, w *sheet.Writer, cols sheet.Columns[Article]) error {
    q := a.db.NewSelect().Model((*Article)(nil)).OrderExpr("created_at DESC")
    if !subject.Can("articles:read-drafts") {
        q = q.WhereGroup(" AND ", func(q *bun.SelectQuery) *bun.SelectQuery {
            return q.Where("status = ?", StatusPublished).WhereOr("author_id = ?", subject.ID)
        })
    }
    rows, err := q.Rows(ctx)
    if err != nil {
        return errors.Wrap(err, "export articles")
    }
    defer rows.Close()
    if err := w.Write(cols.Header()...); err != nil {
        return err
    }
    for rows.Next() {
        var item Article
        if err := a.db.ScanRow(ctx, rows, &item); err != nil {
            return errors.Wrap(err, "scan article")
        }
        if err := w.Write(cols.Row(item)...); err != nil {
            return err
        }
    }
    return errors.Wrap(rows.Err(), "export articles")
}

type ExportInput struct {
    Format  sheet.Format `query:"format" enum:"csv,xlsx" default:"xlsx"`
    Columns []string     `query:"columns" doc:"要导出的列，默认全部"`
}

// HTTP 处理 GET /articles/export。
func (a *Export) HTTP(ctx context.Context, in *ExportInput) (*huma.StreamResponse, error) {
    subject, _ := identity.FromContext(ctx)
    cols, err := exportColumns.Select(in.Columns)
    if err != nil {
        return nil, err
    }
    return download("articles", in.Format, func(w *sheet.Writer) error {
        return a.Handle(ctx, subject, w, cols)
    }), nil
}

// download 以附件形式返回表格，文件名是 name 加上日期和扩展名。
func download(name string, format sheet.Format, write func(w *sheet.Writer) error) *huma.StreamResponse {
    return &huma.StreamResponse{Body: func(hctx huma.Context) {
        filename := name + "-" + time.Now().Format("20060102") + "." + string(format)
        hctx.SetHeader("Content-Type", format.ContentType())
        hctx.SetHeader("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
        w := sheet.NewWriter(hctx.BodyWriter(), format)
        err := errors.CombineErrors(write(w), w.Close())
        if err != nil {
            slog.ErrorContext(hctx.Context(), "export failed", "error", err)
        }
    }}
}
```

导出与[列表查询](authorization.md#scoping-queries)一样，在查询条件中限定 subject 能看到的范围。`q.Rows` 用游标逐行读取，`Writer` 边读边写，导出不会把整张表放进内存。

`GET /articles/export?format=csv&columns=title,published_at` 只导出标题和发布时间两列，顺序与 `columns` 一致。`Select` 在开始写响应之前检查列名，不存在的列返回 `sheet.ErrUnknownColumn`。

> [!WARNING]
> 响应开始写出之后就无法再返回错误状态码。导出中途出错时，客户端收到的是一个不完整的文件，错误只能记录到日志中。所以能提前检查的参数，要像 `Select` 这样在返回 `StreamResponse` 之前检查。

<a id="cell-values"></a>
### 单元格的值

`Value` 返回的值按类型写入单元格：

| 值 | 写出 |
| --- | --- |
| `nil`、nil 指针、零值 `time.Time` | 空单元格 |
| 整数、浮点数、`bool` | XLSX 中保持数值和布尔类型，可以直接求和、筛选 |
| `time.Time` | 显示为 `2006-01-02 15:04:05`，XLSX 中是日期时间单元格 |
| `fmt.Stringer`，例如 `uuid.UUID` | `String()` 的结果 |
| 切片 | 各项以逗号连接 |
| 指针 | 指向的值 |

时间按它自己的时区显示。数据库读出的时间通常是 UTC，要显示为用户的当地时间，在 `Value` 中转换，例如 `a.PublishedAt.In(loc)`。状态等枚举值可以在 `Value` 中用 `i18n.T` 翻译成文字，此时 `Value` 需要 context，把 `exportColumns` 改为接收 `ctx` 的函数即可。

写出的 CSV 以 UTF-8 BOM 开头，Excel 打开时中文不会乱码。以 `=`、`+`、`-`、`@` 开头又不是数字的文本，前面会加一个 `'`，防止 Excel 把它当作公式执行；代价是 `+86 138…` 这样的电话号码在 CSV 中会带上这个 `'`。XLSX 中的文本不会被当作公式，原样写出。XLSX 的表头加粗并冻结在第一行。

<a id="large-files"></a>
## 大文件

同步导入和导出在请求期间占用一个数据库连接，用户也要一直等着。数据量更大时，把工作放到[队列](queue.md)中：导入时先把文件[上传](filesystem.md)到存储，任务用 `Open` 读取文件，再交给同一个 `Import.Handle`；导出时任务把 `sheet.Writer` 写入临时文件，再上传到存储，完成后[通知](notifications.md)用户下载。
