# 全文搜索

- [简介](#introduction)
- [安装](#installation)
- [声明索引](#defining-indexes)
- [同步](#syncing)
    - [一个 key 多个文档](#multiple-documents)
    - [立即同步](#refreshing)
- [重建索引](#reindexing)
- [搜索](#searching)
    - [过滤与排序](#filtering-and-sorting)
    - [分面](#facets)
    - [高亮](#highlighting)
    - [多租户](#tenancy)
- [分词](#tokenizers)

<a id="introduction"></a>
## 简介

`search` 包在应用自己的数据库中提供全文搜索：按关键词查找文章，带过滤、排序、分页、分面统计和命中词高亮。支持 PostgreSQL、SQLite 和 MySQL，三种数据库的结果一致，不需要额外部署搜索服务。

中文是重点。PostgreSQL 自带的全文检索不切分中文，zhparser 之类的扩展在托管数据库上通常装不了；SQLite 的 FTS5 把一串连续的中文当成一个词；MySQL 默认忽略少于 3 个字符的词。`search` 在应用中分词，数据库只保存切分好的词，所以三种数据库都能搜中文，行为也相同。

这个包借鉴自 Laravel 的 [Scout](https://laravel.com/docs/scout)，但不监听模型的保存自动同步：批量更新和原生 SQL 会绕过模型事件，索引就悄悄过期了。同步由 action 在修改数据的事务中显式发起，通过数据库队列执行，事务回滚时同步也一起取消。

<a id="installation"></a>
## 安装

把 `search` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`。同步任务通过数据库队列执行，重建索引的进度记在[后台操作](operations.md)中，所以还需要它们的迁移：

```go
migratecmd.Publish(postgres.Driver,
    migrate.Source{Name: "queue", FS: queue.Migrations()},
    migrate.Source{Name: "operation", FS: operation.Migrations()},
    migrate.Source{Name: "search", FS: search.Migrations()},
)
```

<a id="defining-indexes"></a>
## 声明索引

索引的文档是一个结构体，字段的 `search` 标签说明它的用途。下面是博客应用完整的 `internal/article/search.go`，声明文章索引、读取文档和列出 key 的函数，以及搜索接口：

```go
package article

import (
    "context"
    "time"

    "github.com/cockroachdb/errors"
    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/page"
    "github.com/ryan053857/mygoravel/queue"
    "github.com/ryan053857/mygoravel/search"
)

// SearchDoc 是文章在搜索索引中的文档，只放展示搜索结果需要的字段。
type SearchDoc struct {
    ID          uuid.UUID `json:"id"`
    Slug        string    `json:"slug"`
    Title       string    `json:"title" search:"text,boost"`
    Content     string    `json:"content" search:"text"`
    AuthorID    string    `json:"author_id" search:"filter"`
    PublishedAt time.Time `json:"published_at" search:"filter"`
}

// SearchIndex 是文章的搜索索引。
var SearchIndex = search.Define[SearchDoc]("articles")

// LoadSearchDocs 读取文章当前的状态：已发布的文章返回它的文档，草稿和已删除的文章返回空，从索引中删除。
func LoadSearchDocs(ctx context.Context, tx bun.IDB, key string) ([]SearchDoc, error) {
    id, err := uuid.Parse(key)
    if err != nil {
        return nil, queue.Permanent(err)
    }
    var items []Article
    err = tx.NewSelect().Model(&items).Where("id = ? AND status = ?", id, StatusPublished).Scan(ctx)
    if err != nil {
        return nil, errors.Wrap(err, "load article")
    }
    docs := make([]SearchDoc, len(items))
    for i, item := range items {
        docs[i] = SearchDoc{
            ID: item.ID, Slug: item.Slug, Title: item.Title, Content: item.Content,
            AuthorID: item.AuthorID, PublishedAt: *item.PublishedAt,
        }
    }
    return docs, nil
}

// ScanSearchKeys 按 ID 的顺序列出 after 之后的已发布文章，重建索引时使用。
func ScanSearchKeys(ctx context.Context, db bun.IDB, after string, limit int) ([]string, error) {
    q := db.NewSelect().Model((*Article)(nil)).Column("id").
        Where("status = ?", StatusPublished).OrderExpr("id").Limit(int64(limit))
    if after != "" {
        id, err := uuid.Parse(after)
        if err != nil {
            return nil, queue.Permanent(err)
        }
        q = q.Where("id > ?", id)
    }
    var ids []uuid.UUID
    if err := q.Scan(ctx, &ids); err != nil {
        return nil, errors.Wrap(err, "scan articles")
    }
    keys := make([]string, len(ids))
    for i, id := range ids {
        keys[i] = id.String()
    }
    return keys, nil
}

// Search 搜索已发布的文章。
type Search struct{ db *bun.DB }

func NewSearch(db *bun.DB) *Search { return &Search{db: db} }

type SearchInput struct {
    Q      string `query:"q" maxLength:"100" doc:"Keywords."`
    Author string `query:"author" doc:"Only articles by this author."`
    page.OffsetParams
}

type SearchOutput struct {
    Body *search.Result[SearchDoc]
}

func (a *Search) HTTP(ctx context.Context, in *SearchInput) (*SearchOutput, error) {
    q := search.Query{Text: in.Q, Page: in.OffsetParams}
    if in.Author != "" {
        q.Filter = search.Eq("author_id", in.Author)
    }
    if in.Q == "" {
        q.Sort = []search.Sort{search.Desc("published_at")} // 没有关键词时最新的在前
    }
    res, err := SearchIndex.Search(ctx, a.db, q)
    if err != nil {
        return nil, err
    }
    return &SearchOutput{Body: res}, nil
}
```

标签有三种：

| 标签 | 用途 | 字段类型 |
| --- | --- | --- |
| `text` | 参与全文检索的文本，至少要有一个；`text,boost` 匹配时权重更高，例如标题 | `string`、`[]string` |
| `filter` | 过滤、排序和分面用的值 | 字符串、布尔、整数、浮点数、`time.Time`、实现 `encoding.TextMarshaler` 的类型（如 `uuid.UUID`），以及它们的切片（多个值）和指针（`nil` 为没有值） |
| `tenant` | 文档所在的租户，见[多租户](#tenancy) | `string` |

查询、高亮和分面都按字段的 JSON 名称引用字段，例如 `author_id`。文档整体编码为 JSON 保存在索引中，搜索结果直接解码出 `SearchDoc`，不需要再查一次文章表，所以文档只放展示结果需要的字段。标签写错时 `Define` 直接 panic，程序启动时就能发现。

<a id="syncing"></a>
## 同步

修改文章的 action 在事务中调用 `Sync`，例如[发布文章](actions.md#writing-actions)的 `Handle` 在事务的最后加上一行：

```go
err := database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // ……读取、检查并更新文章
    return SearchIndex.Sync(ctx, tx, item.ID.String())
})
```

`Sync` 只发布一个带着 key 的队列任务，事务提交后才执行。worker 注册同步任务和[重建任务](#reindexing)的处理函数，加在 [worker 的装配](queue.md#running-workers)中：

```go
article.SearchIndex.Handle(w, a.db, article.LoadSearchDocs, article.ScanSearchKeys)
```

处理任务时，`LoadSearchDocs` 在同步事务中读取文章当前的状态，替换索引中这个 key 原有的文档；返回空时从索引中删除。因为任务只带 key、每次都重新读取，重复或乱序执行的结果都一样；同一篇文章连续修改多次，还没开始处理的任务会用 [queue.Unique](queue.md#unique-jobs) 合并为一次。

- 同一个 key 的同步依次执行，后执行的一定读到更新的数据。`LoadFunc` 执行期间持有这个 key 的锁，所以它只应读取数据。
- 删除文章同样调用 `Sync`，`LoadSearchDocs` 读不到文章，索引中的文档随之删除。
- 一个文档最多索引前 `search.MaxTerms`（16383）个词，大约一万多字，之后的文字搜不到。更长的内容按[一个 key 多个文档](#multiple-documents)切分。
- 文档无法建索引时（例如过滤字段的值超过 191 字节），任务不再重试，直接转入[失败任务表](queue.md#failed-jobs)。

修改了 `SearchDoc` 的字段或者[分词器](#tokenizers)之后，已有的文档要按新的方式重新写入，见[重建索引](#reindexing)。

<a id="multiple-documents"></a>
### 一个 key 多个文档

`LoadFunc` 返回的是切片，一个 key 可以对应多个文档。例如[文档转换与切分](documents.md#processing-uploads)中把附件切分为多个分段，以附件 ID 为 key、每个分段一个文档，搜索时命中的是具体的分段，`Hit.Key` 是附件 ID。重新切分后同步，附件原有的分段被整体替换。

<a id="refreshing"></a>
### 立即同步

`Refresh` 不经过队列，立即同步并返回，适合测试和需要马上能搜到的场合：

```go
err := article.SearchIndex.Refresh(ctx, db, article.LoadSearchDocs, item.ID.String())
```

<a id="reindexing"></a>
## 重建索引

修改了 `SearchDoc` 的字段或者[分词器](#tokenizers)，或者有数据绕过 `Sync` 被修改过（例如直接执行的 SQL），就要重建索引。`search/searchcmd` 提供了重建命令，和其他需要数据库的命令一样在 [bootstrap.Commands](actions.md#as-commands) 中声明：

```go
searchcmd.New(a.db, article.SearchIndex)
```

部署新版本之后执行：

```bash
$ ./bin/api search reindex articles
已发起 articles 的重建，操作 0193e7b1-…，由 worker 执行
已处理 500 / 约 12000 个 key
已处理 1000 / 约 12000 个 key
……
重建完成：12000 个 key，其中 0 个无法建索引
```

重建由 worker 执行，命令只负责发起和显示进度，按 Ctrl-C 只停止查看；加上 `--detach` 发起后立即返回。重建写入索引的一个新版本，完成之前查询仍然使用原来的版本，用户不会搜到半个索引：

- 发起之后，同步同时写入原来的版本和新版本，重建期间修改的文章不会漏掉。
- 重建任务用 `ScanSearchKeys` 每次列出 500 个 key，逐个用 `LoadSearchDocs` 读取并写入新版本。`ScanSearchKeys` 返回的 key 读不到文档也没关系，它不会被写入。
- 每处理完一批保存一次进度。worker 重启或者任务出错重试时，从上次保存的地方继续。
- 全部写入后，新版本成为查询使用的版本，原来版本的数据随后删除。

重建期间查询的仍是原来的数据：新加的过滤字段在其中没有值；换了分词器时，查询按新的方式分词，可能搜不全原来的数据。新加的过滤字段可以分两次部署：先部署只修改 `SearchDoc` 的版本并重建，再部署用到这个字段的查询。

同一个索引再次发起重建时，正在进行的重建被取代，它的操作变为失败，错误码是 `search.reindex_superseded`。一个文档无法建索引时，这个 key 计为失败，不影响其他 key。

重建是一个[后台操作](operations.md)，类型是 `search.reindex.articles`。在管理后台发起时直接调用 `Reindex`，操作属于发起的管理员，出现在这个管理员的操作列表中：

```go
op, err := article.SearchIndex.Reindex(ctx, tx, ops, sub)
```

<a id="searching"></a>
## 搜索

`Search` 返回一页结果，`Result` 嵌入了 [page.Offset](responses.md#pagination)，可以直接作为接口的响应体，页面中也可以用它渲染分页导航：

```json
{
  "items": [
    {
      "key": "0193…",
      "doc": {"id": "0193…", "slug": "full-text-search", "title": "全文搜索的设计", "...": "..."},
      "score": 0.61,
      "highlights": {"title": [{"text": "全文"}, {"text": "搜索", "mark": true}, {"text": "的设计"}]}
    }
  ],
  "page": 1, "per_page": 20, "total": 1
}
```

查询文本切分出的每个词都要出现在文档中，最后一个词按前缀匹配，用户输入到一半（`datab`、`搜`）也能搜到。查询文本为空时不按文本过滤，列出符合条件的所有文档。有查询文本时按相关度排序，`text,boost` 字段命中的排在前面；`Score` 在不同数据库中的计算方法不同，只用于比较同一次查询的结果。

<a id="filtering-and-sorting"></a>
### 过滤与排序

`Filter` 用 `Eq`、`In`、`Gt`、`Gte`、`Lt`、`Lte` 比较 `filter` 字段，用 `And`、`Or`、`Not` 组合：

```go
q.Filter = search.And(
    search.In("author_id", authors...),
    search.Gte("published_at", time.Now().AddDate(0, -1, 0)),
)
```

值按字段的类型比较，数字和时间按大小而不是按文字。切片字段只要有一个值满足条件就算满足，`Not(Eq("tags", "go"))` 表示没有一个标签是 `go`。

`Sort` 按 `filter` 字段排序，可以有多个；没有值的文档排在最后。切片字段不能排序。设置了 `Sort` 时先按它排序，再按相关度。

<a id="facets"></a>
### 分面

`Facets` 统计结果中每个值的文档数，用于“按标签筛选（Go 12、Rust 3）”这样的界面：

```go
res, err := SearchIndex.Search(ctx, db, search.Query{Text: in.Q, Facets: []string{"tags"}})
// res.Facets["tags"] 是 []search.FacetValue{{Value: "go", Count: 12}, {Value: "rust", Count: 3}, ...}
```

统计的范围是满足全部查询条件的结果，按文档数从多到少，每个字段最多返回 `FacetLimit` 个值（默认 20）。

<a id="highlighting"></a>
### 高亮

有查询文本时，`Hit.Highlights` 给出命中的文本字段的摘要。摘要按命中与否分成若干段，不需要拼接 HTML，页面中把命中的部分放进 `<mark>`：

```templ
templ snippet(s search.Snippet) {
    for _, p := range s {
        if p.Mark {
            <mark>{ p.Text }</mark>
        } else {
            { p.Text }
        }
    }
}
```

字段不超过 `search.SnippetLength`（160）个字符时摘要就是全文，否则从第一个命中的词前面不远处截取，首尾加省略号。

<a id="tenancy"></a>
### 多租户

文档有 `tenant` 字段时，查询总是只返回 `Query.Tenant` 中的文档，`""` 为平台本身，与[多租户](tenancy.md#data-isolation)中其他数据的约定相同：

```go
type TicketDoc struct {
    ID     uuid.UUID `json:"id"`
    Tenant string    `json:"tenant" search:"tenant"`
    Title  string    `json:"title" search:"text,boost"`
}

res, err := TicketIndex.Search(ctx, db, search.Query{Text: in.Q, Tenant: sub.Tenant})
```

忘记传入租户时查到的是平台本身的文档，不会泄露其他租户的数据。没有 `tenant` 字段的索引传入租户会返回错误。

<a id="tokenizers"></a>
## 分词

默认的 `search.Bigram` 不需要词典：中文、日文和韩文按相邻的两个字切分，“全文搜索”切分为“全文”“文搜”“搜索”；其他文字按连续的字母和数字切分，转为小写，全角转为半角。它不认识词语，查询“文搜”也会命中“全文搜索”，召回多、精度一般，对大多数站内搜索已经够用。

需要按词语切分时使用 `search/gse`，它基于 [gse](https://github.com/go-ego/gse) 的中文词典：

```go
var SearchIndex = search.Define[SearchDoc]("articles", search.WithTokenizer(gse.Tokenizer))
```

建索引时它还输出长词中包含的短词（“搜索引擎”还输出“搜索”“引擎”），查询短词也能命中。代价是程序增大约 25 MB，第一次分词时加载词典约 0.5 秒，常驻内存约 300 MB。

也可以实现 `search.Tokenizer` 接入其他分词方式，`search.Split` 处理了字母、数字和标点，只需要提供切分中日韩文字的函数。更换分词器之后要[重建索引](#reindexing)。
