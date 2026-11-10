# 响应

- [创建响应](#creating-responses)
    - [JSON 响应](#json-responses)
    - [状态码](#status-codes)
    - [响应头](#headers)
- [集合与时间](#collections-and-times)
- [分页](#pagination)
    - [固定分页](#offset-pagination)
    - [游标分页](#cursor-pagination)
- [返回错误](#returning-errors)

<a id="creating-responses"></a>
## 创建响应

<a id="json-responses"></a>
### JSON 响应

和[请求](requests.md)一样，响应也由一个 Go 结构体描述，称为**输出类型**。输出类型中名为 `Body` 的字段就是 JSON 响应体：

```go
type ArticleDTO struct {
    ID          string     `json:"id"`
    Title       string     `json:"title"`
    Slug        string     `json:"slug"`
    AuthorID    string     `json:"author_id"`
    Status      string     `json:"status"`
    PublishedAt *time.Time `json:"published_at,omitempty"`
}

type ArticleOutput struct {
    Body ArticleDTO
}
```

处理器成功时返回输出和 `nil` 错误：

```go
return &ArticleOutput{Body: toDTO(item)}, nil
```

`Body` 中的字段会被编码为 JSON 返回给客户端：

```json
{
  "id": "0f8c2f0e-…",
  "title": "Hello World",
  "slug": "hello-world",
  "author_id": "alice",
  "status": "published",
  "published_at": "2026-11-10T08:30:00Z"
}
```

草稿还没有发布时间，`published_at` 字段会被省略。

> [!TIP]
> 建议为响应定义专门的 DTO 结构，而不是直接返回数据库模型。这样你可以自由地调整数据库结构，而不会意外地改变 API，也不会把内部字段暴露给客户端。

<a id="status-codes"></a>
### 状态码

有响应体的成功响应默认返回 `200`。如果需要返回固定的其他状态码，例如创建资源时的 `201`，可以在路由声明中设置 `DefaultStatus`：

```go
op := huma.Operation{
    OperationID:   "article-create",
    Method:        http.MethodPost,
    Path:          "/articles",
    DefaultStatus: http.StatusCreated,
}
```

如果状态码需要在每次请求时动态决定，可以在输出类型中声明一个 `Status int` 字段，这是 Huma 的原生能力：

```go
type UpsertArticleOutput struct {
    Status int
    Body   ArticleDTO
}
```

<a id="headers"></a>
### 响应头

在输出类型中使用 `header` 标签声明响应头：

```go
type CreatedArticleOutput struct {
    Location string `header:"Location"`
    Body     ArticleDTO
}
```

处理器中设置字段的值即可：

```go
return &CreatedArticleOutput{
    Location: "/articles/" + item.ID.String(),
    Body:     toDTO(item),
}, nil
```

<a id="collections-and-times"></a>
## 集合与时间

列表为空时，应当返回 `[]` 而不是 `null`，这样客户端可以直接遍历而无需判空。在 Go 中，`nil` slice 会被编码为 `null`，所以请初始化一个空 slice：

```go
items := make([]ArticleDTO, 0)
```

时间字段使用 `time.Time`，它会被编码为 RFC 3339 格式的字符串，例如 `"2026-11-10T08:30:00Z"`。建议在业务和数据库中统一使用 UTC 时间，只在展示时转换为用户所在的时区。

<a id="pagination"></a>
## 分页

`page` 包为列表接口提供两种固定的分页结构：

| | 固定分页 | 游标分页 |
| --- | --- | --- |
| 请求参数 | `page`、`per_page` | `cursor`、`per_page` |
| 响应体 | `items`、`page`、`per_page`、`total` | `items`、`next_cursor` |
| 适用场景 | 需要页码和总数，例如后台表格 | 信息流、数据量大或写入频繁的列表 |

两种分页的 `per_page` 都默认为 20、最大为 100，超出范围的请求会被拒绝并返回 `422`。

<a id="offset-pagination"></a>
### 固定分页

把 `page.OffsetParams` 嵌入输入类型，用 `page.Offset[T]` 作为响应体：

```go
type ListArticlesInput struct {
    page.OffsetParams
    Author string `query:"author"`
}

type ListArticlesOutput struct {
    Body page.Offset[ArticleDTO]
}
```

`in.Limit()` 和 `in.Offset()` 直接对应 SQL 的 `LIMIT` 和 `OFFSET`。Bun 的 `ScanAndCount` 可以在同一次调用中读取当前页和总数：

```go
var rows []Article
total, err := db.NewSelect().Model(&rows).
    Where("author_id = ?", in.Author).
    OrderExpr("created_at DESC, id DESC").
    Limit(in.Limit()).
    Offset(in.Offset()).
    ScanAndCount(ctx)
if err != nil {
    return nil, err
}
return &ListArticlesOutput{Body: page.NewOffset(toDTOs(rows), in.OffsetParams, total)}, nil
```

客户端请求 `/articles?page=2&per_page=20` 时得到：

```json
{
  "items": [{"id": "…", "title": "Hello World", "slug": "hello-world", "author_id": "alice", "status": "published"}],
  "page": 2,
  "per_page": 20,
  "total": 41
}
```

<a id="cursor-pagination"></a>
### 游标分页

游标分页从上一页的最后一条继续读取，翻页过程中有新数据写入也不会出现重复或遗漏。把 `page.CursorParams` 嵌入输入类型，用 `page.Cursor[T]` 作为响应体：

```go
type ListPublishedInput struct {
    page.CursorParams
}

type ListPublishedOutput struct {
    Body page.Cursor[ArticleDTO]
}
```

游标里保存的是排序位置，它的结构由你定义。下面按发布时间倒序列出文章，用发布时间和 ID 作为排序位置：

```go
type articleKey struct {
    At time.Time `json:"at"`
    ID uuid.UUID `json:"id"`
}

q := db.NewSelect().Model(&rows).
    Where("status = 'published'").
    OrderExpr("published_at DESC, id DESC").
    Limit(in.Limit())

var after articleKey
if ok, err := in.After(&after); err != nil {
    return nil, err
} else if ok {
    q.Where("(published_at, id) < (?, ?)", after.At, after.ID)
}
if err := q.Scan(ctx); err != nil {
    return nil, err
}

body := page.NewCursor(toDTOs(rows), in.CursorParams, func(a ArticleDTO) any {
    return articleKey{*a.PublishedAt, a.ID}
})
return &ListPublishedOutput{Body: body}, nil
```

这段代码分三步：

1. `in.After` 把请求中的游标解码到 `after`，并报告请求是否携带了游标。第一页没有游标，不添加条件；游标无法解析时返回错误码为 `page.invalid_cursor` 的 `422`。
2. `in.Limit()` 比 `per_page` 多一条，多读的这一条用来判断是否还有下一页。
3. `page.NewCursor` 截掉多读的一条，并把当前页最后一条的排序位置编码为 `next_cursor`。

客户端把 `next_cursor` 原样作为下一次请求的 `cursor` 参数传回；到达最后一页时，响应中不包含 `next_cursor`：

```json
{
  "items": [{"id": "…", "title": "Hello World", "slug": "hello-world", "author_id": "alice", "status": "published"}],
  "next_cursor": "eyJhdCI6IjIwMjYtMTEtMTBUMDg6MzA6MDBaIiwiaWQiOiIwZjhjMmYwZS02YzFkLTRhOGUtOWIxZi0yZDVlN2EzYzliNDAifQ"
}
```

> [!WARNING]
> 排序必须**确定且唯一**，例如 `ORDER BY published_at DESC, id DESC`，并且 `WHERE` 中的比较方向要与排序一致。只按 `published_at` 排序时，同一时间发布的文章可能在翻页时重复或遗漏。

两种分页的响应在没有数据时都返回 `"items": []`，不会出现 `null`。

<a id="returning-errors"></a>
## 返回错误

业务失败时，处理器直接返回 `error` 即可，不需要在输出类型中设计错误字段：

```go
item, err := a.Handle(ctx, subject, in.ID)
if err != nil {
    return nil, err
}
```

`rest.Register` 会把错误转换为统一格式的错误响应，并选择合适的状态码。如何定义业务错误、它们如何映射到 HTTP 状态码，请参阅[错误处理](errors.md)。
