# 响应

- [创建响应](#creating-responses)
    - [JSON 响应](#json-responses)
    - [状态码](#status-codes)
    - [响应头](#headers)
- [集合与时间](#collections-and-times)
- [分页](#pagination)
    - [固定分页](#offset-pagination)
    - [游标分页](#cursor-pagination)
    - [筛选与排序](#filtering-and-sorting)
- [返回错误](#returning-errors)

<a id="creating-responses"></a>
## 创建响应

<a id="json-responses"></a>
### JSON 响应

和[请求](requests.md)一样，响应也由一个 Go 结构体描述，称为**输出类型**。输出类型中名为 `Body` 的字段就是 JSON 响应体：

```go
type ArticleDTO struct {
    ID          string     `json:"id" example:"0193a5c2-7d1e-7b3a-9f61-2c4e8d0a1b2c"`
    Title       string     `json:"title" example:"Hello World"`
    Slug        string     `json:"slug" example:"hello-world"`
    AuthorID    string     `json:"author_id" example:"alice"`
    Status      string     `json:"status" enum:"draft,published" example:"published"`
    PublishedAt *time.Time `json:"published_at,omitempty" doc:"发布时间，草稿没有这个字段" example:"2026-11-10T08:30:00Z"`
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
  "id": "0193a5c2-7d1e-7b3a-9f61-2c4e8d0a1b2c",
  "title": "Hello World",
  "slug": "hello-world",
  "author_id": "alice",
  "status": "published",
  "published_at": "2026-11-10T08:30:00Z"
}
```

草稿还没有发布时间，`published_at` 字段会被省略。`doc`、`example` 和 `enum` 标签只写入接口文档，不影响编码，见[说明与示例](routing.md#describing-apis)。

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

页面中的列表同样使用 `page.OffsetParams` 和 `page.Offset`，`LastPage`、`Links` 用于渲染分页导航，见[列表页](views.md#list-pages)。

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

<a id="filtering-and-sorting"></a>
### 筛选与排序

筛选和排序也是查询参数，和分页参数写在同一个结构体中。下面是文章列表的查询参数，它同时带 `json` 标签，后台的[列表页](views.md#list-pages)用 `web.ParseQuery` 解析同一个结构体：

```go
type ListQuery struct {
    Q      string `query:"q" json:"q,omitempty"`
    Status string `query:"status" json:"status,omitempty" enum:"draft,published"`
    Sort   string `query:"sort" json:"sort,omitempty" default:"-created" enum:"-created,created,title,-title"`
    page.OffsetParams
}
```

`sort` 的值是列名，前面加 `-` 表示降序，和页面上的 [`ui.SortHead`](views.md#list-pages) 一致。Huma 在调用处理器之前检查这些参数：`?sort=views`、`?status=archived` 返回 `422`；空值等同于没有这个参数，取 `default` 的值。

列表 action 把参数转换为查询条件。每个 `sort` 值对应一个固定的 `ORDER BY`：

```go
// orders 是 ListQuery.Sort 每个取值对应的排序。
var orders = map[string]string{
    "created":  "created_at, id",
    "-created": "created_at DESC, id DESC",
    "title":    "title, id",
    "-title":   "title DESC, id DESC",
}

// Handle 返回符合条件的一页文章和总数。
func (a *List) Handle(ctx context.Context, subject identity.Subject, q ListQuery) ([]Article, int64, error) {
    var items []Article
    sel := a.db.NewSelect().Model(&items).
        OrderExpr(orders[q.Sort]).
        Limit(q.Limit()).
        Offset(q.Offset())
    if q.Status != "" {
        sel.Where("status = ?", q.Status)
    }
    if q.Q != "" {
        sel.Where("title LIKE ?", "%"+q.Q+"%")
    }
    total, err := sel.ScanAndCount(ctx)
    if err != nil {
        return nil, 0, errors.Wrap(err, "list articles")
    }
    return items, total, nil
}

type ListOutput struct {
    Body page.Offset[ArticleDTO]
}

// HTTP 处理 GET /articles。
func (a *List) HTTP(ctx context.Context, in *ListQuery) (*ListOutput, error) {
    subject, _ := identity.FromContext(ctx)
    items, total, err := a.Handle(ctx, subject, *in)
    if err != nil {
        return nil, err
    }
    return &ListOutput{Body: page.NewOffset(toDTOs(items), in.OffsetParams, total)}, nil
}
```

`ListQuery` 可以直接作为输入类型。客户端请求 `/articles?status=draft&sort=title&page=2` 时，得到按标题排列的第二页草稿。

几点说明：

- `sort` 的值已经由 `enum` 检查过，`orders[q.Sort]` 一定能查到，客户端传来的字符串不会进入 SQL。增加排序方式时，`enum` 和 `orders` 一起修改。
- 每种排序都以 `id` 结尾，原因与游标分页相同：只按 `title` 排序时，标题相同的文章在两次查询之间的顺序不固定，翻页时可能重复或遗漏。
- 参数为空时不添加对应的条件。按调用者限定范围的条件（例如只看到自己的草稿）写在同一个查询中，见[列表查询中的授权](authorization.md#scoping-queries)。
- `LIKE` 只适合简单的标题匹配。需要分词、按相关度排序时，使用[搜索](search.md)。
- 游标分页的排序固定写在 action 中，不提供 `sort` 参数：游标保存的是排序位置，换一种排序，原来的游标就失效了。需要让用户选择排序的列表，使用固定分页。

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
