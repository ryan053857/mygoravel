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
- [后台导入](#background-imports)
    - [登记导入](#starting-imports)
    - [处理导入](#processing-imports)
    - [进度与失败的行](#progress-and-failures)
    - [清理](#cleanup)
- [后台导出](#background-exports)

<a id="introduction"></a>
## 简介

后台管理系统几乎都要支持用 Excel 批量导入数据，以及把列表导出成表格。`sheet` 包读写 CSV 和 Excel（XLSX）两种格式，导入时按[验证](validation.md)规则检查每一行，规则与 JSON 接口和[页面表单](forms.md)用的是同一个结构体。

本章为博客加上两个功能：从表格批量创建文章，以及把文章列表导出为表格。前两节在一次请求中完成，适合几千行以内的数据；更大的文件放到[后台导入](#background-imports)，进度、断点续跑和失败行的下载由 `imports` 包完成；大量数据同样可以[在后台导出](#background-exports)，完成后通知用户下载。

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
            if err := a.row(ctx, subject, &rec); err != nil {
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

// row 用一行的数据创建文章。slug 已被占用只是这一行的问题，标在 slug 字段上；其他错误原样返回。
func (a *Import) row(ctx context.Context, subject identity.Subject, rec *sheet.Record[Changes]) error {
    _, err := a.create.Handle(ctx, subject, rec.Data)
    if errors.Is(err, ErrSlugTaken) {
        rec.SetError("slug", i18n.T(ctx, ErrSlugTaken.Code(), nil))
        return nil
    }
    return err
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

`row` 把 `Create` 返回的业务错误分两种处理：slug 重复只是这一行的问题，用 `SetError` 放到 `slug` 字段上，接着处理下一行；其他错误（例如没有权限、数据库不可用）对每一行都一样，直接结束导入。[后台导入](#processing-imports)也用这个 `row`。前端拿到的结果类似：

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

<a id="background-imports"></a>
## 后台导入

文件有几万行时，一次请求处理不完，用户也不应该一直等着。`imports` 包把导入放到[队列](queue.md)中：用户上传文件后马上得到一个导入 ID，前端用它查询进度；任务每处理 100 行保存一次进度，重试时从中断的地方继续；没有导入的行保存下来，用户可以下载、改正后重新导入；结束时在同一个事务中通知用户。

导入记录在 `imports` 和 `import_failures` 两张表中，和文件、队列的迁移一起[发布](migrations.md#publishing-framework-migrations)：

```go
migrate.Source{Name: "imports", FS: imports.Migrations()},
```

`imports.New` 在[依赖装配](dependencies.md#assembling-the-application)的 `open` 中创建，`files` 是[文件存储](filesystem.md)的 `*filesystem.Service`，`languages` 是应用的[语言目录](localization.md)：

```go
importer := imports.New(db, files, languages)
```

<a id="starting-imports"></a>
### 登记导入

前端先用[文件上传](filesystem.md)的接口上传表格，再把文件 ID 交给导入接口。`QueueImport` 登记导入，并在同一个事务中发布任务；处理每一行时复用 `Import` 的 `row`。下面是完整的 `internal/article/queue_import.go`：

```go
package article

import (
    "context"

    "github.com/danielgtaylor/huma/v2"
    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/broadcast"
    "github.com/ryan053857/mygoravel/database"
    "github.com/ryan053857/mygoravel/httpx"
    "github.com/ryan053857/mygoravel/httpx/rest"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/imports"
    "github.com/ryan053857/mygoravel/notification"
    "github.com/ryan053857/mygoravel/queue"
    "github.com/ryan053857/mygoravel/sheet"
)

// ImportJob 是后台导入文章的任务，payload 是导入的 ID。
var ImportJob = queue.Topic[uuid.UUID]("articles.import")

// QueueImport 在后台导入文章。
type QueueImport struct {
    db       *bun.DB
    hub      *broadcast.Hub
    importer *imports.Service
    rows     *Import
}

func NewQueueImport(db *bun.DB, hub *broadcast.Hub, importer *imports.Service, rows *Import) *QueueImport {
    return &QueueImport{db: db, hub: hub, importer: importer, rows: rows}
}

// Handle 登记 subject 上传的文件 fileID 的导入，并发布处理它的任务。
func (a *QueueImport) Handle(ctx context.Context, subject identity.Subject, fileID uuid.UUID) (*imports.Import, error) {
    var imp *imports.Import
    err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        var err error
        if imp, err = a.importer.Start(ctx, tx, subject, "articles", fileID); err != nil {
            return err
        }
        return ImportJob.Publish(ctx, queue.Database(tx), imp.ID)
    })
    return imp, err
}

type QueueImportInput struct {
    Body struct {
        FileID uuid.UUID `json:"file_id"`
    }
}

// ImportProgress 是导入的进度，ErrorMessage 是导入失败的原因。
type ImportProgress struct {
    *imports.Import
    ErrorMessage string `json:"error_message,omitempty"`
}

func progressOf(ctx context.Context, imp *imports.Import) ImportProgress {
    return ImportProgress{Import: imp, ErrorMessage: imp.ErrorMessage(ctx)}
}

type ImportProgressOutput struct {
    Body ImportProgress
}

// HTTP 处理 POST /articles/imports。
func (a *QueueImport) HTTP(ctx context.Context, in *QueueImportInput) (*ImportProgressOutput, error) {
    subject, _ := identity.FromContext(ctx)
    imp, err := a.Handle(ctx, subject, in.Body.FileID)
    if err != nil {
        return nil, err
    }
    return &ImportProgressOutput{Body: progressOf(ctx, imp)}, nil
}

// Job 处理后台导入的任务。
func (a *QueueImport) Job(ctx context.Context, id uuid.UUID) error {
    return imports.Process(ctx, a.importer, id, func(ctx context.Context, imp *imports.Import, rec *sheet.Record[Changes]) error {
        return a.rows.row(ctx, imp.Owner(), rec)
    }, a.saved)
}

// importTopic 是一次导入的广播主题。每次保存进度后发布一条空消息，提醒正在查看的连接重新读取进度。
func importTopic(id uuid.UUID) string {
    return "import:" + id.String()
}

// saved 在每次保存进度的事务中调用：提交后广播进度；导入结束时在同一个事务中通知发起导入的人。
func (a *QueueImport) saved(ctx context.Context, tx database.Tx, imp *imports.Import) error {
    tx.AfterCommit(func(ctx context.Context) error {
        return a.hub.Publish(ctx, importTopic(imp.ID), nil)
    })
    if imp.Status == imports.StatusRunning {
        return nil
    }
    _, err := notification.Send(ctx, tx, notification.Notification{
        Event: "articles.imported",
        Data: map[string]any{
            "import_id": imp.ID, "status": imp.Status,
            "created": imp.ProcessedRows - imp.FailedRows, "failed": imp.FailedRows,
        },
    }, notification.RecipientOf(imp.Owner()))
    return err
}

type ImportIDInput struct {
    ID uuid.UUID `path:"id"`
}

// Progress 处理 GET /articles/imports/{id}/progress：连接后先推送当前进度，之后每次保存进度时推送一次，
// 导入结束后关闭连接。
func (a *QueueImport) Progress(ctx context.Context, in *ImportIDInput) (*huma.StreamResponse, error) {
    subject, _ := identity.FromContext(ctx)
    if _, err := a.importer.Get(ctx, subject, in.ID); err != nil {
        return nil, err // 还没有开始推送，客户端照常收到 404
    }
    return rest.SSE(func(s *httpx.Stream) error {
        events := a.hub.Subscribe(importTopic(in.ID)) // 先订阅，再查询，中间保存的进度不会漏掉
        defer events.Close()
        for {
            imp, err := a.importer.Get(s.Context(), subject, in.ID)
            if err != nil {
                return err
            }
            if err := s.SendJSON(httpx.Event{Name: "progress"}, progressOf(s.Context(), imp)); err != nil {
                return nil // 客户端已经离开
            }
            if imp.Status == imports.StatusCompleted || imp.Status == imports.StatusFailed {
                return nil
            }
            select {
            case <-s.Done():
                return nil
            case _, ok := <-events.C():
                if !ok {
                    return nil // 订阅已结束，客户端重连后先收到当前进度
                }
            }
        }
    }), nil
}

type ImportFailuresInput struct {
    ID     uuid.UUID    `path:"id"`
    Format sheet.Format `query:"format" enum:"csv,xlsx" default:"xlsx"`
}

// Failures 处理 GET /articles/imports/{id}/failures，下载没有导入的行。
func (a *QueueImport) Failures(ctx context.Context, in *ImportFailuresInput) (*huma.StreamResponse, error) {
    subject, _ := identity.FromContext(ctx)
    imp, err := a.importer.Get(ctx, subject, in.ID)
    if err != nil {
        return nil, err
    }
    return download("articles-failures", in.Format, func(w *sheet.Writer) error {
        return a.importer.WriteFailures(ctx, imp, w)
    }), nil
}
```

`Start` 确认上传的文件（见[确认文件](filesystem.md#confirming-files)），检查它是 `.csv` 或 `.xlsx`，记下请求的语言，返回 `pending` 状态的导入。任务和导入记录在同一个事务中写入，事务回滚时都不会留下。

<a id="processing-imports"></a>
### 处理导入

worker 注册任务，见[启动 worker](queue.md#running-workers)：

```go
article.ImportJob.Handle(router, sub, article.NewQueueImport(a.db, a.hub, a.importer, article.NewImport(article.NewCreate(a.db))).Job)
```

`imports.Process` 第一次执行时检查表头、统计行数，导入变为 `running`；然后逐行解码，验证通过的行交给回调。回调与同步导入共用 `row`，发起人由 `imp.Owner()` 得到，它只有 ID、类型和租户；`Create` 需要检查权限时，按 ID 重新加载权限；多租户应用中 `Owner` 已经带着租户，见[队列任务](tenancy.md#tenant-jobs)。

每一行的结果有三种：

| 情况 | 结果 |
| --- | --- |
| 验证失败，或者回调用 `SetError` 标出了错误 | 记为没有导入的行 |
| 回调返回带错误码的业务错误 | 同样记为没有导入的行，提示是错误码的译文 |
| 回调返回其他错误，例如数据库不可用 | 保存进度后返回错误，队列稍后重试，从这一行继续 |

验证提示和错误的译文使用登记导入时请求的语言。缺少必需的列、文件损坏等整个文件的问题，导入直接变为 `failed`，`Error` 是错误码；队列的最后一次重试仍然出错时，导入同样变为 `failed`。

`Process` 每次保存进度时都在同一个事务中调用 `saved`：导入变为 `running` 时一次，之后每 100 行一次，结束时再一次。`saved` 用 [AfterCommit](database.md#after-commit) 在提交之后发布广播，提醒正在查看进度的连接，见[进度与失败的行](#progress-and-failures)。导入变为 `completed` 或 `failed` 时，`saved` 在这个事务中发送通知，通知和状态一起提交，不会重复发送，也不会漏发。`notification.Send` 的用法见[通知](notifications.md)。

> [!WARNING]
> 进度每 100 行保存一次。worker 在两次保存之间崩溃时，这些行会在重试时再次交给回调，所以回调要能重复执行。博客的 `row` 遇到已被占用的 slug 会把这一行记为失败，不会重复创建文章；没有唯一字段的数据，可以先按业务字段查找，存在时更新。

<a id="progress-and-failures"></a>
### 进度与失败的行

在 `routes.go` 中注册路由。`Progress` 用 [`rest.RegisterSSE`](sse.md#json-api) 注册，事件表描述推送的数据：

```go
queueImport := NewQueueImport(db, hub, importer, NewImport(NewCreate(db)))

start := huma.Operation{
    OperationID:   "article-import-start",
    Method:        http.MethodPost,
    Path:          "/articles/imports",
    Summary:       "在后台导入文章",
    DefaultStatus: http.StatusAccepted,
}
requireArticles(&start)
rest.Register(api, start, queueImport.HTTP)

progress := huma.Operation{
    OperationID: "article-import-progress",
    Method:      http.MethodGet,
    Path:        "/articles/imports/{id}/progress",
    Summary:     "导入进度",
}
requireArticles(&progress)
rest.RegisterSSE(api, progress, map[string]any{"progress": ImportProgress{}}, queueImport.Progress)
```

`Failures` 的注册方式与 `start` 相同。连接 `GET /articles/imports/{id}/progress` 后，每次保存进度都会收到一个 `progress` 事件：

```text
event: progress
data: {"id":"0193e7a2-7c1e-7b4a-9d2f-3c5a1e8b6f10","kind":"articles","filename":"articles.xlsx","status":"running","total_rows":52000,"processed_rows":18300,"failed_rows":12,"created_at":"2026-11-11T08:00:00Z","updated_at":"2026-11-11T08:00:41Z"}
```

前端用 `processed_rows / total_rows` 显示进度条，收到 `completed` 或 `failed` 后连接关闭，再显示结果和下载失败行的链接。

进度是状态而不是事件：广播只提醒连接重新读取，连接（包括断线重连）时先推送一次当前进度，所以中间漏掉的广播没有影响，也不需要按 `Last-Event-ID` 补发，与[通知角标](notifications.md#realtime)的做法相同。worker 和 SSE 连接在不同的实例上时，广播由 Hub 的后端送达，见[广播](broadcasting.md)。每次读取都经过 `Get`，只有发起导入的人能看到进度，其他人收到 `imports.ErrNotFound`。

浏览器的 `EventSource` 不能设置 `Authorization` 请求头，使用 Bearer 令牌的前端用 `fetch` 读取事件流，见 [JSON API](sse.md#json-api)。

`WriteFailures` 写出的表格是原来的表头加上一列“错误”，每一行是原始单元格和这一行的错误，例如 `标题: 不能为空`。用户在这个文件里改正后可以直接重新导入，多出的错误列会被忽略。需要在页面上直接显示失败的行时，用 `Failures` 读取前几行。

<a id="cleanup"></a>
### 清理

导入记录、失败的行和上传的文件不会自动删除。用[定时任务](scheduling.md#framework-cleanups)定期清理旧的导入，上传的文件被标记为删除，由文件存储的 `Cleanup` 回收：

```go
sched.Add("imports:cleanup", "0 3 * * *", schedule.Cleanup(a.importer.Cleanup(30*24*time.Hour), 1000))
```

<a id="background-exports"></a>
## 后台导出

导出几十万行要几分钟，同样放到队列中：任务把表格写到本地临时文件，上传到[文件存储](filesystem.md)，再[通知](notifications.md)用户下载。导出不需要自己的表：上传的文件处于 temporary 状态，超过 [`temp_ttl`](filesystem.md#configuration)（默认 24 小时）后由文件存储的 `Cleanup` 回收，下载地址也随之失效。

下面是完整的 `internal/article/queue_export.go`，查询和写表仍由[同步导出](#export-action)的 `Export` 完成：

```go
package article

import (
    "context"
    "io"
    "os"
    "time"

    "github.com/cockroachdb/errors"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/filesystem"
    "github.com/ryan053857/mygoravel/identity"
    "github.com/ryan053857/mygoravel/notification"
    "github.com/ryan053857/mygoravel/queue"
    "github.com/ryan053857/mygoravel/sheet"
)

// ExportRequest 是后台导出的任务：谁在导出，导出成什么格式、哪些列。
type ExportRequest struct {
    Subject identity.Subject `json:"subject"`
    Format  sheet.Format     `json:"format"`
    Columns []string         `json:"columns"`
}

// ExportJob 是后台导出文章的任务。
var ExportJob = queue.Topic[ExportRequest]("articles.export")

// QueueExport 在后台导出文章，完成后通知导出的人下载。
type QueueExport struct {
    db     *bun.DB
    files  *filesystem.Service
    export *Export
}

func NewQueueExport(db *bun.DB, files *filesystem.Service, export *Export) *QueueExport {
    return &QueueExport{db: db, files: files, export: export}
}

// Handle 检查要导出的列，然后发布导出任务。
func (a *QueueExport) Handle(ctx context.Context, subject identity.Subject, format sheet.Format, columns []string) error {
    if _, err := exportColumns.Select(columns); err != nil {
        return err
    }
    return ExportJob.Publish(ctx, queue.Database(a.db), ExportRequest{Subject: subject, Format: format, Columns: columns})
}

// HTTP 处理 POST /articles/exports，接受请求后立即返回 202。
func (a *QueueExport) HTTP(ctx context.Context, in *ExportInput) (*struct{}, error) {
    subject, _ := identity.FromContext(ctx)
    return nil, a.Handle(ctx, subject, in.Format, in.Columns)
}

// Job 把文章写到临时文件，上传后通知导出的人。
func (a *QueueExport) Job(ctx context.Context, req ExportRequest) error {
    cols, err := exportColumns.Select(req.Columns)
    if err != nil {
        return queue.Permanent(err)
    }
    tmp, err := os.CreateTemp("", "articles-export-*")
    if err != nil {
        return errors.Wrap(err, "create export file")
    }
    defer os.Remove(tmp.Name())
    defer tmp.Close()

    w := sheet.NewWriter(tmp, req.Format)
    if err := errors.CombineErrors(a.export.Handle(ctx, req.Subject, w, cols), w.Close()); err != nil {
        return err
    }
    size, err := tmp.Seek(0, io.SeekCurrent)
    if err != nil {
        return errors.Wrap(err, "export file size")
    }
    if _, err := tmp.Seek(0, io.SeekStart); err != nil {
        return errors.Wrap(err, "rewind export file")
    }
    file, err := a.files.Upload(ctx, req.Subject, filesystem.FileInput{
        Filename:    "articles-" + time.Now().Format("20060102") + "." + string(req.Format),
        ContentType: req.Format.ContentType(),
        Size:        size,
    }, tmp)
    if err != nil {
        return err
    }
    _, err = notification.Send(ctx, a.db, notification.Notification{
        Event: "articles.exported",
        Data:  map[string]any{"filename": file.Filename, "url": file.URL, "expires_at": file.ExpiresAt},
    }, notification.RecipientOf(req.Subject))
    return err
}
```

`HTTP` 复用同步导出的 `ExportInput`。`Handle` 在发布任务之前检查列名，不存在的列直接返回 `sheet.ErrUnknownColumn`，不会等到任务中才失败。

任务的 payload 带着发起导出的主体，`Export.Handle` 按它的权限限定[查询范围](authorization.md#scoping-queries)，导出的是用户提交请求时能看到的数据；多租户应用中主体也带着租户。

上传的文件不调用 `Confirm`，一直是 temporary 状态，到期后被回收。通知的 `url` 就是文件的[公开地址](filesystem.md#accessing-files)，前端在通知中显示下载链接，`expires_at` 用来提示用户在什么时候之前下载。上传或发通知失败时任务重新执行，重新导出一个文件；没有发出通知的那个文件到期后同样被回收。

对象键是随机的 UUID 加扩展名，浏览器直接打开公开地址时保存的文件名也是它。前端可以用 `<a href={url} download={filename}>` 指定保存的文件名，这个属性只在与页面同源时生效，例如本地磁盘挂在应用自己的路径下。

在 `routes.go` 中注册路由：

```go
exportLater := huma.Operation{
    OperationID:   "article-export-queue",
    Method:        http.MethodPost,
    Path:          "/articles/exports",
    Summary:       "在后台导出文章",
    DefaultStatus: http.StatusAccepted,
}
requireArticles(&exportLater)
rest.Register(api, exportLater, NewQueueExport(db, files, NewExport(db)).HTTP)
```

worker 注册任务：

```go
article.ExportJob.Handle(router, sub, article.NewQueueExport(a.db, a.files, article.NewExport(a.db)).Job)
```
