# 文档转换与切分

- [简介](#introduction)
- [转换文档](#converting)
    - [选择转换器](#choosing-converters)
    - [上限](#limits)
    - [错误](#errors)
- [切分 Markdown](#splitting)
    - [分段的上下文](#chunk-context)
    - [定位原文的页](#locating-pages)
- [在后台处理上传的文档](#processing-uploads)
- [各种格式](#formats)

<a id="introduction"></a>
## 简介

让 AI 助手回答“这份合同的付款条件是什么”，需要先把合同拆成一段段文字，算出向量存起来，提问时找出最相关的几段交给模型。`document` 包负责其中不涉及模型的部分，分两步：

1. **转换**：`document/convert` 把 PDF、Word、PowerPoint、Excel、CSV、HTML、纯文本和 JSON 转换为 Markdown，并记录每一页、每张幻灯片、每个工作表落在 Markdown 的哪个区间。
2. **切分**：`document/split` 把 Markdown 切成长度有上限的分段。中文和英文都在句末、分句标点等自然的位置断开，标题处总会断开，表格和代码只在行之间断开。

两步之间只靠**字节区间**联系：每个分段都是 Markdown 中的 `[Start, End)`，与转换记录的页区间求交集，就知道这一段出自第几页。分段正文与原文逐字节相同，引用时可以直接高亮原文。

<a id="converting"></a>
## 转换文档

`convert/all` 注册了全部转换器。PDF 转换器持有 PDFium 实例，在[入口](dependencies.md#assembling-the-application)中创建一次并登记关闭：

```go
import "github.com/go-tenon/tenon/document/convert/all"

converters := all.New(all.Options{})
stack.MustAdd("documents", func(context.Context) error { return converters.Close() })
```

按文件名确定格式，再转换：

```go
format, _ := convert.FormatOf(file.Filename) // "report.PDF" → convert.PDF
doc, err := converters.Convert(ctx, format, convert.Input{Reader: r, Name: file.Filename})
```

`doc.Markdown` 是转换结果，`doc.Sections` 是各页（幻灯片、工作表）在其中的区间。转换器可以并发使用；PDF 默认最多同时转换 2 份，用 `all.Options{PDF: pdf.Options{Workers: 4}}` 调整。

中文的纯文本、CSV 和 HTML 有时是 GBK 编码。没有 BOM、也没有声明编码的输入不是合法的 UTF-8 时，用 `Fallback` 解码：

```go
converters := all.New(all.Options{Fallback: simplifiedchinese.GB18030})
```

<a id="choosing-converters"></a>
### 选择转换器

PDF 转换器使用编译为 WebAssembly 的 PDFium，不需要 cgo，但会让程序大几 MB；Excel 和 HTML 也各自引入了第三方库。只需要部分格式时，自己创建 `convert.Registry`，只注册需要的转换器，没有引入的子包不会编译进程序：

```go
var converters convert.Registry
converters.Register(text.New(text.Options{}), convert.Text, convert.Markdown)
converters.Register(docx.New(docx.Options{}), convert.DOCX)
```

`Registry` 的零值就可以使用。它不会关闭注册的转换器，自己注册的 `pdf.Converter` 要自己关闭。

<a id="limits"></a>
### 上限

文档通常由用户上传，可能非常大，也可能是精心构造的压缩炸弹。每个转换器都有上限，超出时返回错误：

| 上限 | 限制的对象 | 默认值 |
| --- | --- | --- |
| `Limits.MaxBytes` | 读取的原件字节数 | 32 MiB |
| `Limits.MaxExpandedBytes` | Word、PowerPoint、Excel 解压后的字节数，以及 HTML 展开合并单元格的复制量 | 128 MiB |
| `Limits.MaxOutputBytes` | 生成的 Markdown 字节数 | 64 MiB |
| `pdf.Options.MaxPages` | PDF 的页数 | 10000 |
| `pdf.Options.MaxPageChars` | PDF 一页的字符数 | 1 Mi |

零值取默认值，负数表示不限制。`all.Options.Limits` 设置所有转换器的上限，每个转换器自己的 `Options.Limits` 可以覆盖其中的字段。输出在生成过程中就计入上限，不会先生成远超上限的内容再失败。

<a id="errors"></a>
### 错误

文档本身有问题时，转换返回的错误包装了以下四种之一，它们都属于 `errorx.InvalidArgument`：action 直接返回时得到 `422` 和翻译好的提示，例如“文档设置了密码，无法读取”。

| 错误 | 含义 |
| --- | --- |
| `convert.ErrUnsupported` | 没有这种格式的转换器，包括旧的 `.doc`、`.xls`、`.ppt` |
| `convert.ErrEncrypted` | 文档设置了打开密码 |
| `convert.ErrCorrupt` | 文档已损坏，或者不是声明的格式；二进制格式的空文件也是这种错误 |
| `convert.ErrTooLarge` | 超出了上面的某个上限，`errors.As` 取出 `*convert.LimitError` 可以知道是哪一个 |

在队列任务中遇到这四种错误时，用 `queue.Permanent` 标记，重试也不会成功，见[下文](#processing-uploads)。读取失败、PDFium 启动失败等其他错误照常重试。

<a id="splitting"></a>
## 切分 Markdown

```go
import "github.com/go-tenon/tenon/document/split"

splitter, err := split.New(split.Options{Size: 500, Overlap: 50})
if err != nil {
    return err // 选项不合法
}
for _, chunk := range splitter.Split(doc.Markdown) {
    // chunk.Text 是 doc.Markdown[chunk.Start:chunk.End]
}
```

| 选项 | 含义 |
| --- | --- |
| `Size` | 目标长度，必填 |
| `MaxSize` | 长度的硬上限，默认 `Size + Size/5`：略长于 `Size` 的小节保持完整，不会留下很短的尾段 |
| `Overlap` | 与上一段重叠的上限，必须小于 `Size`；在标题处或找不到合适的边界时不重叠 |

长度按码点计算，一个汉字和一个英文字母都算 1，与模型的 token 数并不相等。中文资料的 `Size` 通常取 300 到 800。`Splitter` 不可变，可以在多个 goroutine 中共用。

切分器用 [goldmark](https://github.com/yuin/goldmark) 按 CommonMark 和 GFM 表格识别块结构，另外识别 YAML front matter；用 goldmark 渲染同一份 Markdown 时，两边认定的标题和表格相同。准确的规则、每条保证和病态输入的上限见 `document/split` 的包文档（`go doc github.com/go-tenon/tenon/document/split`）。

与常见的切分器相比：langchaingo 的 `MarkdownTextSplitter` 会重新渲染 Markdown，默认的分隔符也切不开没有空格的中文段落；eino-ext 的标题切分器会去掉每行的首尾空白和空行，代码缩进因此丢失，表格分段也不带表头；两者都不记录分段在原文中的位置。

<a id="chunk-context"></a>
### 分段的上下文

只看“付款应在验收后 30 日内完成”这一段，模型不知道它说的是哪份合同的哪一章。每个分段带着它所在的标题路径 `Headings`，从表格中间开始的分段还带着表头 `TableHeader`。`TextWithContext` 把它们放在正文前面，适合用来计算向量：

```
采购合同 > 第三章 付款

付款应在验收后 30 日内完成……
```

正文 `Text` 保持原样，存储和展示时仍然用它。上下文的长度不受 `Size` 限制，很长的标题会完整地出现在它下面的每一段中；模型对输入长度有要求时，自己用 `Headings` 拼接或截断。

<a id="locating-pages"></a>
### 定位原文的页

`doc.SectionsIn(chunk.Start, chunk.End)` 返回分段涉及的页（幻灯片、工作表），回答问题时可以注明“见第 3–4 页”：

```go
var pages []int
for _, s := range doc.SectionsIn(chunk.Start, chunk.End) {
    pages = append(pages, s.Number)
}
```

`Number` 从 1 开始，与原件的页码一致，空白页和跳过的隐藏幻灯片也计数。Word 和 HTML 没有固定的页，`Sections` 为空。

<a id="processing-uploads"></a>
## 在后台处理上传的文档

博客允许作者给文章上传附件，AI 助手回答读者问题时可以引用附件的内容。附件通过[文件存储](filesystem.md)上传，确认时发布一个队列任务，见[确认后异步处理](filesystem.md#processing-files)：

```go
type IndexJob struct {
    ArticleID uuid.UUID `json:"article_id"`
    FileID    uuid.UUID `json:"file_id"`
}

var IndexAttachment = queue.Topic[IndexJob]("attachments.index")

err := IndexAttachment.Publish(ctx, tx, IndexJob{ArticleID: item.ID, FileID: file.ID})
```

任务读取文件，转换、切分，在一个事务中替换这个附件原有的分段，并发起[向量索引](vector.md)的同步：

```go
func (a *Index) Job(ctx context.Context, job IndexJob) error {
    r, file, err := a.files.Open(ctx, job.FileID)
    if err != nil {
        return err
    }
    defer r.Close()
    format, _ := convert.FormatOf(file.Filename)
    doc, err := a.converters.Convert(ctx, format, convert.Input{Reader: r, Name: file.Filename})
    if errorx.KindOf(err) == errorx.InvalidArgument {
        return queue.Permanent(err) // 加密、损坏、太大：重试也不会成功
    }
    if err != nil {
        return err
    }

    chunks := a.splitter.Split(doc.Markdown)
    rows := make([]AttachmentChunk, len(chunks))
    for i, c := range chunks {
        rows[i] = AttachmentChunk{
            ArticleID: job.ArticleID, FileID: job.FileID, Position: i,
            Text: c.Text, Context: c.TextWithContext(), Start: c.Start, End: c.End, Pages: pagesOf(doc, c),
        }
    }
    return database.RunInTx(ctx, a.db, nil, func(ctx context.Context, tx database.Tx) error {
        if _, err := tx.NewDelete().Model((*AttachmentChunk)(nil)).Where("file_id = ?", job.FileID).Exec(ctx); err != nil {
            return err
        }
        if len(rows) > 0 { // 没有文字的文档，例如扫描件，没有分段
            if _, err := tx.NewInsert().Model(&rows).Exec(ctx); err != nil {
                return err
            }
        }
        return Chunks.Sync(ctx, tx, job.FileID.String())
    })
}
```

- 任务可能重复执行，先删后插让结果保持一致。
- `Context` 是带标题路径的正文，用来计算向量；`Text` 是原文，引用时展示。
- 向量在同步任务中计算并写入 Qdrant，文本没变的分段沿用原来的向量。`Chunks` 的声明和检索见[向量检索](vector.md#defining-indexes)。
- 多租户应用中，分段表和向量索引的文档都带上租户，见[向量检索的多租户](vector.md#tenancy)。

<a id="formats"></a>
## 各种格式

| 格式 | 转换结果 | Section |
| --- | --- | --- |
| 纯文本、Markdown | 解码和规范化换行后原样输出 | 无 |
| JSON | 放进 `json` 代码围栏，只在行之间切分 | 无 |
| HTML | CommonMark 加 GFM 表格；合并单元格展开为矩形，相对链接按 `Input.BaseURL` 解析 | 无 |
| CSV | 一个 GFM 表格，第一行是表头（`NoHeader` 时都是数据） | 无 |
| Word | 标题来自标题样式和大纲级别，编号段落成为列表，表格、外部链接保留 | 无 |
| PowerPoint | 每张幻灯片的标题、文本框、表格、图表数据和演讲者备注；隐藏的幻灯片默认跳过 | 每张幻灯片一个 |
| Excel | 每个工作表一个二级标题和一个表格，单元格显示格式化后的值；隐藏的工作表默认跳过 | 每个工作表一个 |
| PDF | 按字号识别标题和段落，阅读顺序沿用 PDFium 的文字顺序 | 每页一个 |

几点限制：

- PDF 不还原多栏排版和表格；没有文字层的扫描件得到空的 Markdown，需要 OCR 时先用其他服务识别。只设置了权限密码、打开不需要密码的 PDF 照常转换。
- 不支持旧的二进制格式 `.doc`、`.xls`、`.ppt`，返回 `convert.ErrUnsupported`。
- 不提取图片，也不调用任何模型。

`convert.Converter` 只有一个方法，其他格式可以自己实现并注册到 `Registry`，输出 Markdown 后照样用 `split` 切分。
