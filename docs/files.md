# 文件存储与上传

- [简介](#introduction)
- [配置](#configuration)
- [磁盘](#disks)
    - [本地磁盘](#local-disk)
    - [S3 磁盘](#s3-disk)
- [装配文件服务](#assembling-files)
- [通过应用服务器上传](#server-upload)
- [客户端直传](#direct-upload)
    - [单次上传](#direct-put)
    - [分片上传](#multipart-upload)
- [确认文件](#confirming-files)
- [访问文件](#accessing-files)
- [删除文件](#deleting-files)
- [过期清理](#cleanup)
- [表结构](#schema)

<a id="introduction"></a>
## 简介

`files` 用一张 `files` 表记录应用中的每个文件，文件正文保存在**磁盘**上：本地目录或 S3 兼容的对象存储。所有磁盘都通过公开地址访问，地址就是磁盘的 `public_url` 拼接对象键，读取文件不需要签名。

一个文件的生命周期是：

1. **上传**：通过应用服务器上传（`Upload`），或由客户端直传到 S3（`Prepare` 后签名上传）。此时文件是 `temporary` 状态。
2. **确认**：业务真正使用这个文件时（例如保存文章、更新头像）调用 `Confirm`，文件变为 `ready`。
3. **回收**：超过 `temp_ttl` 仍未确认的文件，以及已删除文件的对象，由 `Cleanup` 定期清理。

先上传、后确认，用户上传后放弃表单时，临时文件会自动回收，不会在存储中留下垃圾。

使用前先发布 `files` 表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(
    migrate.Source{Name: "files", FS: files.Migrations()},
))
```

<a id="configuration"></a>
## 配置

文件服务和每个磁盘各有自己的配置类型，放进应用的根配置结构体：

```go
import (
    "github.com/ryan053857/mygoravel/files"
    "github.com/ryan053857/mygoravel/files/local"
    "github.com/ryan053857/mygoravel/files/s3"
)

type Config struct {
    Database database.Config `yaml:"database"`
    Files    files.Config    `yaml:"files"`
    Storage  struct {
        Local local.Config `yaml:"local"`
        S3    s3.Config    `yaml:"s3"`
    } `yaml:"storage"`
}
```

```yaml
# config/app.yaml
files:
  default_disk: local
  max_upload_size: 1GiB
  temp_ttl: 24h

storage:
  local:
    root: storage/files
    public_url: ${APP_URL}/storage
  s3:
    bucket: blog-files
    region: ap-southeast-1
    endpoint: ${S3_ENDPOINT:-}
    path_style: false
    access_key: ${S3_ACCESS_KEY:-}
    secret_key: ${S3_SECRET_KEY:-}
    public_url: https://cdn.example.com
```

`files` 段的字段：

| 配置 | 默认值 | 含义 |
| --- | --- | --- |
| `default_disk` | `local` | 未指定磁盘时使用的磁盘名 |
| `max_upload_size` | `1GiB` | 单个文件的最大大小，可写 `512MiB`、`2GiB` 或字节数 |
| `temp_ttl` | `24h` | 上传后多久未确认就被回收，至少 `1m` |

只用一种磁盘时，只保留对应的配置段即可。

<a id="disks"></a>
## 磁盘

磁盘是 `files.Disk` 接口的实现，负责保存对象和生成公开地址。框架提供本地磁盘和 S3 磁盘。

<a id="local-disk"></a>
### 本地磁盘

| 配置 | 含义 |
| --- | --- |
| `root` | 存储目录，由应用独占，不存在时自动创建 |
| `public_url` | 公开访问基址，可以是绝对 URL，也可以是以 `/` 开头的路径（前后端同域时） |

`local.Open` 打开磁盘。磁盘本身是一个 `http.Handler`，挂在 `public_url` 的路径上就能提供下载：

```go
disk, err := local.Open(cfg.Storage.Local)
if err != nil {
    return err
}
defer disk.Close()

router.Handle("/storage/*", disk)
```

它会去掉 `public_url` 的路径前缀，再按对象键读取文件；不列目录，也不暴露正在写入的临时文件。文件按扩展名设置 `Content-Type`，并支持 `Range` 和缓存校验。

写入先落到同目录的临时文件，完整写入后再原子重命名，上传失败不会留下半个文件。多个进程共享同一个本地磁盘时，它们需要挂载同一个目录。

<a id="s3-disk"></a>
### S3 磁盘

| 配置 | 含义 |
| --- | --- |
| `bucket` | 桶名，需要预先创建 |
| `region` | 区域；留空时读取 AWS 环境配置 |
| `endpoint` | S3 兼容服务（MinIO、R2、OSS 等）的 API 地址，AWS 留空 |
| `path_style` | 使用 path-style 访问桶，MinIO 通常需要 |
| `access_key` / `secret_key` | 静态密钥；留空时使用 AWS 默认凭证链 |
| `public_url` | 公开访问基址，通常是 CDN 或桶的公开域名，必填 |

```go
disk, err := s3.Open(ctx, cfg.Storage.S3)
```

`Open` 会检查桶是否可以访问。你需要为桶（或 CDN 回源）配置公开读，让 `public_url` 加对象键能直接访问文件。`endpoint` 是程序调用的 API 地址，`public_url` 是给用户访问的地址，两者可以不同。

已经有原生 SDK 客户端时，可以用 `s3.New(client, bucket, publicURL)` 创建磁盘；`disk.Client()` 返回原生客户端，供你使用未封装的 S3 功能。

浏览器直传还需要为桶配置 CORS：允许你的站点来源、`PUT` 方法和签名返回的请求头，并暴露 `ETag` 响应头（分片上传需要读取它）。

<a id="assembling-files"></a>
## 装配文件服务

`files.New` 接收数据库、配置和磁盘。磁盘 map 的键是磁盘名，会写入 `files.disk` 字段，之后读取文件时按这个名字找到磁盘，所以名字一旦使用就要保持稳定：

```go
localDisk, err := local.Open(cfg.Storage.Local)
if err != nil {
    return err
}
s3Disk, err := s3.Open(ctx, cfg.Storage.S3)
if err != nil {
    return err
}
fileService, err := files.New(db, cfg.Files, map[string]files.Disk{
    "local": localDisk,
    "s3":    s3Disk,
})
if err != nil {
    return err
}
```

把 `fileService` 传给需要文件的业务服务和处理器。文件服务不注册路由，也不启动后台任务：上传接口、权限检查和清理调度都由应用决定。

<a id="server-upload"></a>
## 通过应用服务器上传

最简单的方式是让应用服务器接收文件并写入磁盘。`Upload` 适用于所有磁盘，正文以流的方式写入，不会整体读进内存：

```go
file, header, err := r.FormFile("file")
if err != nil {
    return err
}
defer file.Close()

record, err := fileService.Upload(ctx, owner, files.FileInput{
    Filename:    header.Filename,
    ContentType: header.Header.Get("Content-Type"),
    Size:        header.Size,
}, file)
if err != nil {
    return err
}
```

`owner` 是当前请求的 `identity.Subject`，见[认证](authentication.md)。正文的实际长度必须正好等于 `Size`，否则返回 `files.ErrInvalid`，磁盘上也不会留下文件。`Disk` 为空时使用 `default_disk`，也可以指定 `Disk: "s3"`。

返回的 `files.Record` 已经带有公开地址 `record.URL`，可以直接返回给前端预览。它的 `JSON` 形式如下：

```json
{
  "id": "0193a3c2-...",
  "disk": "local",
  "key": "2026/11/10/0193a3c2-....jpg",
  "filename": "photo.jpg",
  "content_type": "image/jpeg",
  "size": 102400,
  "status": "temporary",
  "expires_at": "2026-11-11T08:00:00Z",
  "url": "https://blog.example.com/storage/2026/11/10/0193a3c2-....jpg"
}
```

对象键由上传日期、文件 ID 和小写扩展名组成，原始文件名只保存在 `filename` 字段中。

<a id="direct-upload"></a>
## 客户端直传

使用 S3 磁盘时，客户端可以拿着签名地址把文件直接传到 S3，正文不经过应用服务器。先用 `Prepare` 登记文件，再根据大小选择单次上传或分片上传：

```go
record, err := fileService.Prepare(ctx, owner, files.FileInput{
    Disk:        "s3",
    Filename:    "video.mp4",
    ContentType: "video/mp4",
    Size:        size,
})
```

签名返回 `files.SignedRequest`，包含 `url`、`method` 和必须原样携带的 `headers`。签名绑定了文件的大小和类型，长度不符的请求会被 S3 拒绝。签名有效期是 15 分钟，并且不超过临时文件剩余的时间。

本地磁盘不支持直传，签名方法返回 `files.ErrUnsupported`，请改用 `Upload`。

<a id="direct-put"></a>
### 单次上传

不超过 5GiB 的文件可以一次上传：

```go
signed, err := fileService.SignPut(ctx, owner, record.ID)
```

客户端用 `signed` 发起请求：

```js
await fetch(signed.url, { method: signed.method, headers: signed.headers, body: file })
```

上传成功后，业务使用文件时调用 [Confirm](#confirming-files)。

<a id="multipart-upload"></a>
### 分片上传

大文件使用分片上传，单片失败只需重传这一片。先创建分片会话：

```go
upload, err := fileService.CreateMultipart(ctx, owner, record.ID)
// upload.UploadID、upload.PartSize、upload.PartCount
```

分片大小默认 16MiB，超大文件会自动放大，保证不超过 S3 的 10000 片上限，不需要配置。除最后一片外，每片正好 `PartSize` 字节。会话 ID 不保存在数据库中，由客户端保存并在后续请求中带回。

每一片上传前，单独为它签名，签名后立即使用，因此网络再慢也不会遇到签名过期：

```go
signed, err := fileService.SignPart(ctx, owner, record.ID, uploadID, number) // number 从 1 开始
```

客户端上传每一片后，从响应头读取 `ETag` 并保存。不同分片可以并发上传；某一片失败时，重新签名并重传这一片即可。全部上传后，提交编号和 ETag 合并：

```go
record, err := fileService.CompleteMultipart(ctx, owner, record.ID, uploadID, []files.Part{
    {Number: 1, ETag: `"9b2cf535f27731c974343645a3985328"`},
    {Number: 2, ETag: `"6f5902ac237024bdd0c176cb93063dc4"`},
})
```

`parts` 必须正好覆盖 `1..PartCount`。合并成功但响应丢失时，重复调用同样返回成功。放弃上传时调用 `AbortMultipart(ctx, owner, id, uploadID)`，之后可以重新创建会话。

<a id="confirming-files"></a>
## 确认文件

上传完成的文件仍是 `temporary`。业务真正使用它时调用 `Confirm`，例如把文件设为文章封面：

```go
cover, err := fileService.Confirm(ctx, owner, input.CoverID)
if err != nil {
    return err
}
article.CoverURL = cover.URL
```

`Confirm` 会检查对象确实存在且大小与登记的一致，然后把文件改为 `ready`。重复确认返回同一条记录。只有上传者本人可以确认，其他主体会得到 `files.ErrNotFound`，因此用户无法引用他人上传的临时文件。

希望文件状态和业务数据一起提交时，使用 `WithDB` 在业务事务中确认：

```go
err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
    cover, err := fileService.WithDB(tx).Confirm(ctx, owner, input.CoverID)
    if err != nil {
        return err
    }
    _, err = tx.NewUpdate().Model(&article).Set("cover_id = ?", cover.ID).WherePK().Exec(ctx)
    return err
})
```

事务回滚时文件保持 `temporary`，到期后被回收。

<a id="accessing-files"></a>
## 访问文件

文件是公开的，访问地址就是 `record.URL`。通常你会把文件 ID 保存在业务表中，返回数据时查出记录：

```go
record, err := fileService.Get(ctx, article.CoverID)
if err != nil {
    return err
}
// record.URL
```

`Get` 只按 ID 查询，不检查归属；需要权限控制的数据应由业务在调用前检查。如果只需要拼地址，也可以把 `key` 保存在业务表中，用磁盘的 `URL(key)` 生成。

需要在服务端处理文件内容（例如生成缩略图）时，用 `Open` 读取正文，调用方负责关闭：

```go
body, record, err := fileService.Open(ctx, id)
if err != nil {
    return err
}
defer body.Close()
```

<a id="deleting-files"></a>
## 删除文件

```go
err := fileService.Delete(ctx, id)
```

`Delete` 先把记录标为 `deleted`，再删除磁盘上的对象和记录。文件不存在时也返回成功，可以放心重复调用。删除对象失败时返回错误，记录保持 `deleted`，由 `Cleanup` 稍后重试。

在事务中删除时（`WithDB(tx).Delete`），只标记删除，事务提交后由 `Cleanup` 删除对象；事务回滚则文件原样保留，不会出现记录还在、对象已经被删的情况。

<a id="cleanup"></a>
## 过期清理

`Cleanup` 回收到期的记录，应用通过已有的调度器或命令定期调用它，例如每小时一次：

```go
n, err := fileService.Cleanup(ctx, 500)
```

它最多处理 `limit` 条记录，返回成功清理的数量：

- 超过 `temp_ttl` 仍未确认的 `temporary` 文件：中止未完成的分片会话，删除对象和记录；
- 已删除但对象尚未清理的文件：删除对象和记录。

某条记录清理失败时，它会推迟 5 分钟再重试，错误合并返回，不影响其他记录。正在被确认的文件不会被误删：清理和确认通过带条件的更新互斥，只有一方能成功。

> [!NOTE]
> 预签名地址无法撤销：如果客户端在文件被清理前拿到了签名，并在清理之后才开始上传，对象会重新出现在 S3 中。签名有效期只有 15 分钟，远短于 `temp_ttl`，这种情况很少见。需要彻底兜底时，可以为桶配置[未完成分片的生命周期规则](https://docs.aws.amazon.com/AmazonS3/latest/userguide/mpu-abort-incomplete-mpu-lifecycle-config.html)。

<a id="schema"></a>
## 表结构

| 字段 | 含义 |
| --- | --- |
| `id` | UUIDv7 |
| `disk / key` | 磁盘名和对象键，联合唯一 |
| `filename / content_type / size` | 原始文件名、类型和大小 |
| `owner_type / owner_id` | 上传者的主体类型和 ID |
| `status` | `temporary`、`ready` 或 `deleted` |
| `expires_at` | `temporary` 的回收时间，`deleted` 的清理重试时间；`ready` 为空 |
| `created_at / updated_at` | 创建和更新时间 |

你可以为 `files` 表增加列，框架只读写上面这些列。所有状态变更都是带条件的单条 `UPDATE`，存储读写不占用数据库事务，多个进程同时操作同一个文件也是安全的。

服务返回的错误可以用 `errors.Is` 匹配：

| 错误 | 含义 |
| --- | --- |
| `files.ErrInvalid` | 输入不合法，例如正文长度与 `Size` 不符、分片编号错误 |
| `files.ErrTooLarge` | 超过 `max_upload_size` |
| `files.ErrNotFound` | 文件不存在、已删除、已过期，或不属于当前主体 |
| `files.ErrNotUploaded` | 确认时对象还没有上传完整 |
| `files.ErrUnsupported` | 磁盘不支持客户端直传 |
