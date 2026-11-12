# 向量检索

- [简介](#introduction)
- [安装](#installation)
- [连接 Qdrant](#connecting)
- [声明索引](#defining-indexes)
- [同步](#syncing)
    - [沿用向量](#reusing-vectors)
    - [立即同步](#refreshing)
- [重建索引](#reindexing)
- [检索](#searching)
    - [过滤](#filtering)
    - [多租户](#tenancy)
    - [混合检索](#hybrid-search)
    - [重排](#reranking)
- [换模型](#changing-models)
- [测试](#testing)

<a id="introduction"></a>
## 简介

[全文搜索](search.md)按关键词查找，读者问“付款要在多久内完成”，写着“验收后 30 日内结清”的那一段就搜不到。向量检索按语义查找：把每一段文字用模型转换为向量，提问时找出向量最接近的几段，交给模型回答。

`vector` 包把向量保存在 [Qdrant](https://qdrant.tech) 中，不放进 PostgreSQL。数据仍然以数据库为准，Qdrant 中只是由它算出来的副本。用法与[全文搜索](search.md)相同：action 在修改数据的事务中调用 `Sync`，队列任务重新读取数据、计算向量，然后写入 Qdrant。事务回滚时，同步也一起取消。

<a id="installation"></a>
## 安装

```bash
go get github.com/qdrant/go-client@v1.19.0
```

把 `vector` 的迁移加入[发布命令](migrations.md#publishing-framework-migrations)，发布后执行 `migrate up`。同步任务通过[队列](queue.md)执行，重建索引的进度记在[后台操作](operations.md)中，所以还需要它们的迁移：

```go
migratecmd.Publish(
    migrate.Source{Name: "queue", FS: queue.Migrations()},
    migrate.Source{Name: "operation", FS: operation.Migrations()},
    migrate.Source{Name: "vector", FS: vector.Migrations()},
)
```

数据库中只有两张小表：`vector_keys` 让同一个 key 的同步依次执行，`vector_indexes` 记录索引的版本。向量和文档都在 Qdrant 中。

<a id="connecting"></a>
## 连接 Qdrant

mygoravel 不封装 Qdrant 客户端，直接使用官方的 [go-client](https://github.com/qdrant/go-client)，它走 gRPC 端口 6334。配置由应用自己定义：

```go
type QdrantConfig struct {
    Host   string `yaml:"host"`
    Port   int    `yaml:"port"`
    APIKey string `yaml:"api_key"`
    TLS    bool   `yaml:"tls"`
}

type Config struct {
    // ...
    Qdrant    QdrantConfig   `yaml:"qdrant"`
    Embedding ai.ModelConfig `yaml:"embedding"`
}
```

在[入口](dependencies.md#assembling-the-application)的 `open` 中创建客户端和 `vector.Store`，并登记关闭：

```go
qc, err := qdrant.NewClient(&qdrant.Config{
    Host: cfg.Qdrant.Host, Port: cfg.Qdrant.Port, APIKey: cfg.Qdrant.APIKey, UseTLS: cfg.Qdrant.TLS,
})
if err != nil {
    return nil, err
}
stack.MustAdd("qdrant", func(context.Context) error { return qc.Close() })

vectors := vector.New(db, qc, vector.Model{
    Name:     cfg.Embedding.Model,
    Dims:     1024,
    Embedder: openai.New(cfg.Embedding, openai.WithBatchSize(10)),
})
```

`Store` 持有数据库，同步时用它锁住 key、读取文档。`Model` 用来计算向量：`Dims` 必须与模型输出的维度一致；`Embedder` 是 [ai.Embedder](ai.md#media)，分段多时用 `openai.WithBatchSize` 分批请求。

多个应用共用一个 Qdrant 时，用 `vector.WithPrefix("blog_")` 给 collection 名称加前缀。

<a id="defining-indexes"></a>
## 声明索引

[文档转换与切分](documents.md#processing-uploads)把文章的附件切分为分段，存进 `attachment_chunks` 表（模型 `AttachmentChunk`）。下面是完整的 `internal/attachment/vector.go`，它声明分段的向量索引、读取文档和列出 key 的函数：

```go
package attachment

import (
    "context"

    "github.com/cockroachdb/errors"
    "github.com/google/uuid"
    "github.com/uptrace/bun"

    "github.com/ryan053857/mygoravel/queue"
    "github.com/ryan053857/mygoravel/vector"
)

// ChunkDoc 是一个分段在向量索引中的文档，只放回答问题需要的字段。
type ChunkDoc struct {
    ArticleID uuid.UUID `json:"article_id" vector:"filter"`
    FileID    uuid.UUID `json:"file_id" vector:"filter"`
    Text      string    `json:"text"`
    Pages     []int     `json:"pages"`
    Context   string    `json:"-" vector:"embed"` // 带标题路径的正文，只用来计算向量
}

// Chunks 是附件分段的向量索引，key 是附件 ID。
var Chunks = vector.Define[ChunkDoc]("attachment-chunks")

// LoadChunks 读取附件当前的分段，附件删除后返回空，从索引中删除。
func LoadChunks(ctx context.Context, tx bun.IDB, key string) ([]ChunkDoc, error) {
    fileID, err := uuid.Parse(key)
    if err != nil {
        return nil, queue.Permanent(err)
    }
    var rows []AttachmentChunk
    err = tx.NewSelect().Model(&rows).Where("file_id = ?", fileID).Order("position").Scan(ctx)
    if err != nil {
        return nil, errors.Wrap(err, "load chunks")
    }
    docs := make([]ChunkDoc, len(rows))
    for i, r := range rows {
        docs[i] = ChunkDoc{ArticleID: r.ArticleID, FileID: r.FileID, Text: r.Text, Pages: r.Pages, Context: r.Context}
    }
    return docs, nil
}

// ScanChunkKeys 按 ID 的顺序列出 after 之后有分段的附件，重建索引时使用。
func ScanChunkKeys(ctx context.Context, db bun.IDB, after string, limit int) ([]string, error) {
    q := db.NewSelect().Model((*AttachmentChunk)(nil)).ColumnExpr("DISTINCT file_id").
        OrderExpr("file_id").Limit(int64(limit))
    if after != "" {
        id, err := uuid.Parse(after)
        if err != nil {
            return nil, queue.Permanent(err)
        }
        q = q.Where("file_id > ?", id)
    }
    var ids []uuid.UUID
    if err := q.Scan(ctx, &ids); err != nil {
        return nil, errors.Wrap(err, "scan chunk keys")
    }
    keys := make([]string, len(ids))
    for i, id := range ids {
        keys[i] = id.String()
    }
    return keys, nil
}
```

标签有三种：

| 标签 | 用途 | 字段类型 |
| --- | --- | --- |
| `embed` | 计算向量的文本，有且只有一个；只用来计算、不需要保存时写 `json:"-"` | `string` |
| `filter` | 过滤用的值，见[过滤](#filtering) | 字符串、布尔、整数、浮点数、`time.Time`、实现 `encoding.TextMarshaler` 的类型（如 `uuid.UUID`），以及它们的切片和指针 |
| `tenant` | 文档所在的租户，见[多租户](#tenancy) | `string` |

一个 key 可以对应多个文档：这里以附件 ID 为 key，每个分段一个文档。文档整体编码为 JSON 保存在 Qdrant 中，检索结果直接解码出 `ChunkDoc`，不需要再查一次分段表。标签写错时 `Define` 直接 panic，程序启动时就能发现。

<a id="syncing"></a>
## 同步

处理附件的任务在替换分段的同一个事务中调用 `Sync`，见[在后台处理上传的文档](documents.md#processing-uploads)：

```go
return database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
    // ……删除附件原有的分段，写入新的
    return Chunks.Sync(ctx, tx, job.FileID.String())
})
```

worker 注册同步任务和[重建任务](#reindexing)的处理函数，加在 [worker 的装配](queue.md#running-workers)中：

```go
attachment.Chunks.Handle(w, a.vectors, attachment.LoadChunks, attachment.ScanChunkKeys)
```

处理任务时，`LoadChunks` 在同步事务中读取附件当前的分段，计算向量后替换 Qdrant 中这个 key 原有的文档；返回空时从索引中删除。任务只带 key，每次都重新读取，重复或乱序执行的结果都一样。

- 同一个 key 的同步依次执行，后执行的一定读到更新的数据。为此锁要一直持有到写完 Qdrant，计算向量期间同步事务也保持打开，所以 `LoadFunc` 只应读取数据，转换、切分文件这类耗时的处理放在之前的任务中完成。
- 删除附件同样调用 `Sync`，`LoadChunks` 读不到分段，索引中的文档随之删除。
- 计算向量的文本为空时，任务不再重试，直接转入[失败任务表](queue.md#failed-jobs)。模型服务出错时，任务按队列的规则重试。
- 索引的每个版本是 Qdrant 中的一个 collection，第一次写入时自动创建，同时给过滤字段建立索引；之后新增过滤字段时，进程启动后第一次同步会补建索引。

<a id="reusing-vectors"></a>
### 沿用向量

计算向量要花钱。同步时先查出这个 key 已有的文档，计算向量的文本与已有文档相同的直接沿用原来的向量，只把新的文本交给模型；同一次同步中相同的文本只计算一次。作者改了附件中的一句话，只有那一段重新计算，即使后面的分段位置都变了。

<a id="refreshing"></a>
### 立即同步

`Refresh` 不经过队列，立即同步并返回，适合测试和需要马上能查到的场合：

```go
err := attachment.Chunks.Refresh(ctx, vectors, attachment.LoadChunks, fileID.String())
```

<a id="reindexing"></a>
## 重建索引

换了模型、改了计算向量的文本（例如 `Context` 的拼法），或者有数据绕过 `Sync` 被修改过，就要重建索引。重建命令与[全文搜索](search.md#reindexing)共用 `search/searchcmd`，把向量索引一起传进去，在 [bootstrap.Commands](actions.md#as-commands) 中声明：

```go
searchcmd.New(a.db, article.SearchIndex, attachment.Chunks)
```

部署新版本之后执行：

```bash
$ ./bin/api search reindex attachment-chunks
已发起 attachment-chunks 的重建，操作 0193e7b1-…，由 worker 执行
已处理 500 / 约 3200 个 key
……
重建完成：3200 个 key，其中 0 个无法建索引
```

重建写入索引的一个新版本，也就是一个新的 collection，完成之前查询仍然使用原来的版本：

- 发起之后，同步同时写入原来的版本和新版本，重建期间修改的附件不会漏掉。
- 重建任务用 `ScanChunkKeys` 每次列出 500 个 key，逐个用 `LoadChunks` 读取，计算向量后写入新版本。进度保存在操作中，任务重试时从上次保存的地方继续。
- 全部写入后，新版本成为查询使用的版本，原来版本的 collection 随后删除。

新版本中的每个文档都要重新计算一次向量，重建前估算一下费用。同一个索引再次发起重建时，正在进行的重建被取代，它的操作变为失败，错误码是 `vector.reindex_superseded`。重建是一个[后台操作](operations.md)，类型是 `vector.reindex.attachment-chunks`，在管理后台发起时直接调用 `attachment.Chunks.Reindex(ctx, tx, ops, sub)`。

<a id="searching"></a>
## 检索

`Search` 用 `Store` 的模型计算查询文本的向量，返回语义最接近的文档，按相似度从高到低排列。下面是 AI 助手查找文章附件内容的工具：

```go
type Find struct{ vectors *vector.Store }

func NewFind(vectors *vector.Store) *Find { return &Find{vectors: vectors} }

// Find 返回文章附件中与问题最相关的分段。
func (a *Find) Find(ctx context.Context, articleID uuid.UUID, question string) ([]vector.Hit[ChunkDoc], error) {
    return Chunks.Search(ctx, a.vectors, vector.Query{
        Text:   question,
        Filter: vector.Eq("article_id", articleID),
        Limit:  20,
    })
}
```

每条结果 `Hit` 带有 `Key`（附件 ID）、解码后的 `Doc` 和 `Score`。`Score` 是余弦相似度，越大越接近。`Limit` 默认为 `vector.DefaultLimit`（10）；`MinScore` 可以去掉相似度太低的结果，但合适的阈值因模型而异，先看看实际的分数再定。还没有同步过任何文档时，`Search` 返回空。

<a id="filtering"></a>
### 过滤

过滤条件按字段的 JSON 名称引用 `filter` 字段，与[全文搜索的过滤](search.md#filtering-and-sorting)写法相同：

```go
vector.And(
    vector.Eq("article_id", articleID),
    vector.Not(vector.In("file_id", hidden...)),
)
```

- `Eq`、`In` 适用于所有类型；`Gt`、`Gte`、`Lt`、`Lte` 只适用于数字和时间。
- 多值字段（切片）只要有一个值满足条件就算满足。
- 值会转换为字段的类型，例如 `int` 字段可以传 `int64`。字段不存在、类型不对时 `Search` 返回错误。

Qdrant 在检索的同时过滤，返回的是满足条件的文档中最接近的 `Limit` 条，不会像先检索、再过滤那样把结果滤掉一大半。

<a id="tenancy"></a>
### 多租户

文档类型有 `tenant` 字段时，检索总是限定在 `Query.Tenant` 中，传入 `sub.Tenant`：

```go
hits, err := Chunks.Search(ctx, a.vectors, vector.Query{Text: question, Tenant: sub.Tenant})
```

`Tenant` 为空表示平台本身，也只查询租户为空的文档，不会查到所有租户的。没有 `tenant` 字段的索引，传入租户时返回错误。

有租户字段的索引，Qdrant 中的数据按租户存放，HNSW 图也按租户分别建立，不建全局的图，所以查询一个租户的速度不受其他租户数据量的影响。

<a id="hybrid-search"></a>
### 混合检索

语义检索不认识具体的字：读者问“X-2000 的保修期”，向量最接近的可能是另一个型号的保修说明。型号、编号、人名、专有名词这类问题，关键词检索更准。把索引声明为混合索引，检索时两路同时进行：

```go
var Chunks = vector.Define[ChunkDoc]("attachment-chunks", vector.Hybrid(search.Bigram))
```

- 同步时，`embed` 字段的文本除了交给模型，还用[分词器](search.md#tokenizers)切分，按 BM25 的词频部分算出稀疏向量，和模型的向量存在同一个文档上。IDF 由 Qdrant 按整个索引统计，有租户字段时也是所有租户一起统计。
- 检索时，查询文本同样切分，语义和关键词两路各取若干候选，带着相同的过滤条件和租户，再在 Qdrant 中按两路的排名用 RRF 融合。只在一路中靠前的文档也能排进结果。
- 中文用 `search.Bigram` 就能用，不需要词典；需要按词语切分时换成 `search/gse`。

混合索引的 `Hit.Score` 是 RRF 分数，不再是余弦相似度，只能比较同一次查询的结果；`MinScore` 只过滤语义那一路的候选。

给已有的索引加上或去掉 `Hybrid`、换分词器之后，要[重建索引](#reindexing)。重建完成之前，原来的版本照常写入和查询，只是还没有关键词那一路。

<a id="reranking"></a>
### 重排

向量检索擅长找出大致相关的分段，排序却不够精细。多取一些候选，再用[重排模型](ai.md#media)排出最相关的几段交给模型：

```go
hits, err := a.find.Find(ctx, articleID, question)
if err != nil {
    return nil, err
}
hits, err = ai.RerankBy(ctx, a.reranker, question, hits, func(h vector.Hit[ChunkDoc]) string {
    return h.Doc.Text
}, 5)
```

<a id="changing-models"></a>
## 换模型

不同模型算出的向量不能比较。索引的每个版本都记录了建立它的模型名称和维度，写入和查询这个版本时总是用这个模型计算向量。`Store` 中没有这个模型时，同步和检索都返回错误，而不会把两种向量混在一起。

名称或维度变了都算换模型，例如同一个模型用 `openai.WithDimensions` 从 3072 维改为 1024 维。换模型分三步：

1. 把配置改为新模型，旧模型用 `vector.WithPrevious` 传入，部署：

    ```go
    vectors := vector.New(db, qc,
        vector.Model{Name: cfg.Embedding.Model, Dims: 1536, Embedder: openai.New(cfg.Embedding)},
        vector.WithPrevious(vector.Model{Name: cfg.OldEmbedding.Model, Dims: 1024, Embedder: openai.New(cfg.OldEmbedding)}),
    )
    ```

    这时查询和同步仍然使用旧模型建立的版本。
2. 执行 `search reindex attachment-chunks`。新版本用新模型建立，重建期间的修改同时用两个模型计算。
3. 重建完成后，去掉 `WithPrevious` 和旧模型的配置，再部署一次。

Qdrant 中的数据都可以从数据库重新算出来，所以不必和数据库一起备份。丢失后重建索引即可，只是要再付一次计算向量的费用。

<a id="testing"></a>
## 测试

`vector/vectortest` 创建连接测试 Qdrant 的 `Store`，地址来自环境变量 `QDRANT_TEST_ADDR`（例如 `127.0.0.1:6334`）。每次调用使用随机的 collection 前缀，测试结束时删除。计算向量用 [aitest.Embedder](ai.md#testing)，相同的文本总是得到相同的向量：

```go
func TestFind(t *testing.T) {
    ctx := context.Background()
    db := dbtest.Open(t, migrations.FS)
    vectors := vectortest.New(t, db, vector.Model{Name: "test", Dims: 64, Embedder: aitest.NewEmbedder(64)})
    // ……写入文章、附件和分段
    if err := attachment.Chunks.Refresh(ctx, vectors, attachment.LoadChunks, fileID.String()); err != nil {
        t.Fatal(err)
    }

    hits, err := attachment.NewFind(vectors).Find(ctx, articleID, "验收后 30 日内结清")
    if err != nil {
        t.Fatal(err)
    }
    if len(hits) == 0 || hits[0].Doc.Text != "验收后 30 日内结清" {
        t.Fatalf("hits = %+v", hits)
    }
}
```

`aitest.Embedder` 给不同的文本几乎正交的向量，只有原文完全相同的查询才排在第一。它只能用来测试同步、过滤和租户是否正确，测不了语义检索的效果。
