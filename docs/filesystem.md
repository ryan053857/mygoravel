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
- [在 REST 接口中使用](#rest)
    - [路由](#rest-routes)
    - [服务端上传接口](#rest-upload)
    - [直传接口](#rest-direct-upload)
    - [在业务接口中确认](#rest-confirm)
    - [请求体大小与超时](#rest-limits)
    - [错误响应](#rest-errors)
- [过期清理](#cleanup)
- [表结构](#schema)

<a id="introduction"></a>
## 简介

`filesystem` 用一张 `files` 表记录应用中的每个文件，文件正文保存在**磁盘**上：本地目录或 S3 兼容的对象存储。所有磁盘都通过公开地址访问，地址就是磁盘的 `public_url` 拼接对象键，读取文件不需要签名。

一个文件的生命周期是：

1. **上传**：通过应用服务器上传（`Upload`），或由客户端直传到 S3（`Prepare` 后签名上传）。此时文件是 `temporary` 状态。
2. **确认**：业务真正使用这个文件时（例如保存文章、更新头像）调用 `Confirm`，文件变为 `ready`。
3. **回收**：超过 `temp_ttl` 仍未确认的文件，以及已删除的文件，由清理任务 `Cleanup` 从存储中删除。

先上传、后确认，用户上传后放弃表单时，临时文件会自动回收，不会在存储中留下垃圾。

使用前先发布 `files` 表的迁移，见[发布框架迁移](migrations.md#publishing-framework-migrations)：

```go
migrationCmd.AddCommand(migratecmd.Publish(
    migrate.Source{Name: "filesystem", FS: filesystem.Migrations()},
))
```

<a id="configuration"></a>
## 配置

文件服务的配置和各个磁盘的配置放在同一个 `filesystem` 段中：

```yaml
# config/app.yaml
filesystem:
  default_disk: local
  max_upload_size: 1GiB
  temp_ttl: 24h
  disks:
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

在应用中定义对应的配置结构体：内联 `filesystem.Config`，再在 `disks` 下列出你使用的磁盘：

```go
import (
    "github.com/ryan053857/mygoravel/filesystem"
    "github.com/ryan053857/mygoravel/filesystem/local"
    "github.com/ryan053857/mygoravel/filesystem/s3"
)

type FilesystemConfig struct {
    filesystem.Config `yaml:",inline"`
    Disks             struct {
        Local local.Config `yaml:"local"`
        S3    s3.Config    `yaml:"s3"`
    } `yaml:"disks"`
}

type Config struct {
    Database   database.Config  `yaml:"database"`
    Filesystem FilesystemConfig `yaml:"filesystem"`
}
```

磁盘由应用自己列出，所以只用一种磁盘时只写一个字段；需要第二个桶时，再加一个 `Backup s3.Config` 即可。每个配置类型都有自己的默认值和校验，`config.Load` 会逐一检查，错误会指出具体位置，例如 `filesystem.disks.s3: bucket is required`。

`filesystem` 段本身的字段：

| 配置 | 默认值 | 含义 |
| --- | --- | --- |
| `default_disk` | `local` | 未指定磁盘时使用的磁盘名 |
| `max_upload_size` | `1GiB` | 单个文件的最大大小，可写 `512MiB`、`2GiB` 或字节数 |
| `temp_ttl` | `24h` | 上传后多久未确认就被回收，至少 `1m` |

<a id="disks"></a>
## 磁盘

磁盘是 `filesystem.Disk` 接口的实现，负责保存对象和生成公开地址。框架提供本地磁盘和 S3 磁盘。

<a id="local-disk"></a>
### 本地磁盘

| 配置 | 含义 |
| --- | --- |
| `root` | 存储目录，由应用独占，不存在时自动创建 |
| `public_url` | 公开访问基址，可以是绝对 URL，也可以是以 `/` 开头的路径（前后端同域时） |

`local.Open` 打开磁盘。磁盘本身是一个 `http.Handler`，挂在 `public_url` 的路径上就能提供下载：

```go
disk, err := local.Open(cfg.Filesystem.Disks.Local)
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
disk, err := s3.Open(ctx, cfg.Filesystem.Disks.S3)
```

`Open` 会检查桶是否可以访问。你需要为桶（或 CDN 回源）配置公开读，让 `public_url` 加对象键能直接访问文件。`endpoint` 是程序调用的 API 地址，`public_url` 是给用户访问的地址，两者可以不同。

已经有原生 SDK 客户端时，可以用 `s3.New(client, bucket, publicURL)` 创建磁盘；`disk.Client()` 返回原生客户端，供你使用未封装的 S3 功能。

浏览器直传还需要为桶配置 CORS：允许你的站点来源、`PUT` 方法和签名返回的请求头，并暴露 `ETag` 响应头（分片上传需要读取它）。

<a id="assembling-files"></a>
## 装配文件服务

`filesystem.New` 接收数据库、配置和磁盘。磁盘 map 的键是磁盘名，会写入 `files.disk` 字段，之后读取文件时按这个名字找到磁盘，所以名字一旦使用就要保持稳定：

```go
localDisk, err := local.Open(cfg.Filesystem.Disks.Local)
if err != nil {
    return err
}
s3Disk, err := s3.Open(ctx, cfg.Filesystem.Disks.S3)
if err != nil {
    return err
}
fileService, err := filesystem.New(db, cfg.Filesystem.Config, map[string]filesystem.Disk{
    "local": localDisk,
    "s3":    s3Disk,
})
if err != nil {
    return err
}
```

把 `fileService` 传给需要文件的业务服务和处理器。文件服务不注册路由，也不启动后台任务：上传接口和权限检查由应用决定，清理由[清理任务](#cleanup)执行。

<a id="server-upload"></a>
## 通过应用服务器上传

最简单的方式是让应用服务器接收文件并写入磁盘。`Upload` 适用于所有磁盘，正文以流的方式写入，不会整体读进内存：

```go
file, header, err := r.FormFile("file")
if err != nil {
    return err
}
defer file.Close()

record, err := fileService.Upload(ctx, owner, filesystem.FileInput{
    Filename:    header.Filename,
    ContentType: header.Header.Get("Content-Type"),
    Size:        header.Size,
}, file)
if err != nil {
    return err
}
```

`owner` 是当前请求的 `identity.Subject`，见[认证](authentication.md)。正文的实际长度必须正好等于 `Size`，否则返回 `filesystem.ErrInvalid`，磁盘上也不会留下文件。`Disk` 为空时使用 `default_disk`，也可以指定 `Disk: "s3"`。

返回的 `filesystem.File` 已经带有公开地址 `record.URL`，可以直接返回给前端预览。它的 `JSON` 形式如下：

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
record, err := fileService.Prepare(ctx, owner, filesystem.FileInput{
    Disk:        "s3",
    Filename:    "video.mp4",
    ContentType: "video/mp4",
    Size:        size,
})
```

签名返回 `filesystem.SignedRequest`，包含 `url`、`method` 和必须原样携带的 `headers`。签名绑定了文件的大小和类型，长度不符的请求会被 S3 拒绝。签名有效期是 15 分钟，并且不超过临时文件剩余的时间。

本地磁盘不支持直传，签名方法返回 `filesystem.ErrUnsupported`，请改用 `Upload`。

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
record, err := fileService.CompleteMultipart(ctx, owner, record.ID, uploadID, []filesystem.Part{
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

`Confirm` 会检查对象确实存在且大小与登记的一致，然后把文件改为 `ready`。重复确认返回同一条记录。只有上传者本人可以确认，其他主体会得到 `filesystem.ErrNotFound`，因此用户无法引用他人上传的临时文件。

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

`Delete` 把记录标为 `deleted`，此后 `Get`、`Open` 和 `Confirm` 都把它视为不存在。文件不存在时也返回成功，可以放心重复调用。

磁盘上的对象和记录由[清理任务](#cleanup)删除，`Delete` 本身只是一条 `UPDATE`，不访问存储。因此它可以放进业务事务中，与业务数据一起提交：

```go
err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
    if _, err := tx.NewDelete().Model(&article).WherePK().Exec(ctx); err != nil {
        return err
    }
    return fileService.WithDB(tx).Delete(ctx, article.CoverID)
})
```

事务回滚时文件原样保留。注意在清理任务执行之前，对象仍然可以通过公开地址访问。

<a id="rest"></a>
## 在 REST 接口中使用

文件服务不注册路由，上传接口由你在 [REST](routing.md) 模块中按需声明。本节给出博客应用中 `filehttp` 模块的完整写法，目录组织与[组织路由](routing.md#organizing-routes)一致：

```text
internal/file/filehttp/
├── routes.go              注册文件模块的路由
└── actions/
    ├── upload_file.go     服务端上传
    ├── direct_upload.go   S3 直传：登记、签名、分片
    └── dto.go             共享的输入输出类型
```

<a id="rest-routes"></a>
### 路由

| 方法和路径 | 服务方法 | 用途 |
| --- | --- | --- |
| `POST /files` | `Upload` | 通过应用服务器上传（`multipart/form-data`） |
| `POST /files/prepare` | `Prepare` | 登记直传文件 |
| `POST /files/{id}/sign` | `SignPut` | 单次直传签名 |
| `POST /files/{id}/multipart` | `CreateMultipart` | 创建分片会话 |
| `POST /files/{id}/multipart/{uploadID}/parts/{number}/sign` | `SignPart` | 为一片签名 |
| `POST /files/{id}/multipart/{uploadID}/complete` | `CompleteMultipart` | 合并分片 |
| `DELETE /files/{id}/multipart/{uploadID}` | `AbortMultipart` | 放弃分片上传 |

只使用本地磁盘时，保留第一个路由即可。`Confirm` 不需要单独的接口，它在使用文件的业务接口中调用，见[在业务接口中确认](#rest-confirm)。

`routes.go` 声明这些路由。所有接口都需要登录，`requireUser` 由入口传入，例如 `rest.RequireBearer(api, validateToken)`：

```go
package filehttp

import (
    "context"
    "net/http"

    "github.com/danielgtaylor/huma/v2"

    "example.com/blog/internal/file/filehttp/actions"
    "github.com/ryan053857/mygoravel/filesystem"
    "github.com/ryan053857/mygoravel/httpx/rest"
)

// Register 注册文件模块的全部路由。
func Register(api *rest.API, requireUser func(*huma.Operation), files *filesystem.Service) {
    upload := huma.Operation{
        OperationID:     "file-upload",
        Method:          http.MethodPost,
        Path:            "/files",
        Summary:         "上传文件",
        DefaultStatus: http.StatusCreated,
    }
    requireUser(&upload)
    rest.Register(api, upload, func() func(context.Context, *actions.UploadFileInput) (*actions.FileOutput, error) {
        return actions.NewUploadFile(files).Handle
    })

    prepare := huma.Operation{
        OperationID:   "file-prepare",
        Method:        http.MethodPost,
        Path:          "/files/prepare",
        Summary:       "登记直传文件",
        DefaultStatus: http.StatusCreated,
    }
    requireUser(&prepare)
    rest.Register(api, prepare, func() func(context.Context, *actions.PrepareFileInput) (*actions.FileOutput, error) {
        return actions.NewDirectUpload(files).Prepare
    })

    signPut := huma.Operation{
        OperationID: "file-sign-put",
        Method:      http.MethodPost,
        Path:        "/files/{id}/sign",
        Summary:     "单次直传签名",
    }
    requireUser(&signPut)
    rest.Register(api, signPut, func() func(context.Context, *actions.FileIDInput) (*actions.SignedOutput, error) {
        return actions.NewDirectUpload(files).SignPut
    })

    createMultipart := huma.Operation{
        OperationID:   "file-multipart-create",
        Method:        http.MethodPost,
        Path:          "/files/{id}/multipart",
        Summary:       "创建分片会话",
        DefaultStatus: http.StatusCreated,
    }
    requireUser(&createMultipart)
    rest.Register(api, createMultipart, func() func(context.Context, *actions.FileIDInput) (*actions.MultipartOutput, error) {
        return actions.NewDirectUpload(files).CreateMultipart
    })

    signPart := huma.Operation{
        OperationID: "file-multipart-sign-part",
        Method:      http.MethodPost,
        Path:        "/files/{id}/multipart/{uploadID}/parts/{number}/sign",
        Summary:     "分片签名",
    }
    requireUser(&signPart)
    rest.Register(api, signPart, func() func(context.Context, *actions.SignPartInput) (*actions.SignedOutput, error) {
        return actions.NewDirectUpload(files).SignPart
    })

    complete := huma.Operation{
        OperationID: "file-multipart-complete",
        Method:      http.MethodPost,
        Path:        "/files/{id}/multipart/{uploadID}/complete",
        Summary:     "合并分片",
    }
    requireUser(&complete)
    rest.Register(api, complete, func() func(context.Context, *actions.CompleteMultipartInput) (*actions.FileOutput, error) {
        return actions.NewDirectUpload(files).CompleteMultipart
    })

    abort := huma.Operation{
        OperationID:   "file-multipart-abort",
        Method:        http.MethodDelete,
        Path:          "/files/{id}/multipart/{uploadID}",
        Summary:       "放弃分片上传",
        DefaultStatus: http.StatusNoContent,
    }
    requireUser(&abort)
    rest.Register(api, abort, func() func(context.Context, *actions.UploadIDInput) (*actions.NoContentOutput, error) {
        return actions.NewDirectUpload(files).AbortMultipart
    })
}
```

入口在组装应用时调用它：

```go
filehttp.Register(api, rest.RequireBearer(api, validateToken), fileService)
```

`actions/dto.go` 定义共享的输入和输出。响应体直接使用 `filesystem.File` 等类型，它们自带 JSON 标签，`File` 的 JSON 中包含公开地址 `url`，但不包含上传者信息：

```go
package actions

import (
    "github.com/google/uuid"
    "github.com/ryan053857/mygoravel/filesystem"
)

type FileIDInput struct {
    ID uuid.UUID `path:"id"`
}

type UploadIDInput struct {
    ID       uuid.UUID `path:"id"`
    UploadID string    `path:"uploadID"`
}

type FileOutput struct {
    Body filesystem.File
}

type SignedOutput struct {
    Body filesystem.SignedRequest
}

type MultipartOutput struct {
    Body filesystem.Multipart
}

type NoContentOutput struct{}
```

<a id="rest-upload"></a>
### 服务端上传接口

`actions/upload_file.go` 用 Huma 的 `MultipartFormFiles` 接收 `multipart/form-data` 中名为 `file` 的字段，再交给 `Upload`：

```go
package actions

import (
    "context"

    "github.com/danielgtaylor/huma/v2"
    "github.com/ryan053857/mygoravel/filesystem"
    "github.com/ryan053857/mygoravel/identity"
)

type UploadFileInput struct {
    RawBody huma.MultipartFormFiles[struct {
        File huma.FormFile `form:"file" required:"true"`
    }]
}

type UploadFile struct {
    files *filesystem.Service
}

func NewUploadFile(files *filesystem.Service) *UploadFile {
    return &UploadFile{files: files}
}

func (a *UploadFile) Handle(ctx context.Context, in *UploadFileInput) (*FileOutput, error) {
    owner, _ := identity.FromContext(ctx)
    f := in.RawBody.Data().File
    file, err := a.files.Upload(ctx, owner, filesystem.FileInput{
        Filename:    f.Filename,
        ContentType: f.ContentType,
        Size:        f.Size,
    }, f.File)
    if err != nil {
        return nil, err
    }
    return &FileOutput{Body: file}, nil
}
```

Huma 解析表单时，较小的文件留在内存中，较大的文件写入临时文件，`Upload` 再把它流式写入磁盘。错误直接返回即可，`filesystem` 的错误会转换为对应的 HTTP 响应，见[错误响应](#rest-errors)。

客户端上传：

```bash
curl -X POST https://blog.example.com/files \
  -H "Authorization: Bearer $TOKEN" \
  -F "file=@cover.png"
```

```json
{
  "id": "0193a3c2-...",
  "disk": "local",
  "key": "2026/11/10/0193a3c2-....png",
  "filename": "cover.png",
  "content_type": "image/png",
  "size": 102400,
  "status": "temporary",
  "expires_at": "2026-11-11T08:00:00Z",
  "url": "https://blog.example.com/storage/2026/11/10/0193a3c2-....png"
}
```

<a id="rest-direct-upload"></a>
### 直传接口

`actions/direct_upload.go` 把直传相关的几个动作放在一起，它们都只是把路径参数转交给文件服务：

```go
package actions

import (
    "context"

    "github.com/google/uuid"
    "github.com/ryan053857/mygoravel/filesystem"
    "github.com/ryan053857/mygoravel/identity"
)

type PrepareFileInput struct {
    Body struct {
        Filename    string `json:"filename" minLength:"1"`
        ContentType string `json:"content_type,omitempty"`
        Size        int64  `json:"size" minimum:"0"`
    }
}

type SignPartInput struct {
    ID       uuid.UUID `path:"id"`
    UploadID string    `path:"uploadID"`
    Number   int32     `path:"number" minimum:"1" maximum:"10000"`
}

type CompleteMultipartInput struct {
    ID       uuid.UUID `path:"id"`
    UploadID string    `path:"uploadID"`
    Body     struct {
        Parts []filesystem.Part `json:"parts" minItems:"1"`
    }
}

type DirectUpload struct {
    files *filesystem.Service
}

func NewDirectUpload(files *filesystem.Service) *DirectUpload {
    return &DirectUpload{files: files}
}

func (a *DirectUpload) Prepare(ctx context.Context, in *PrepareFileInput) (*FileOutput, error) {
    owner, _ := identity.FromContext(ctx)
    file, err := a.files.Prepare(ctx, owner, filesystem.FileInput{
        Disk:        "s3",
        Filename:    in.Body.Filename,
        ContentType: in.Body.ContentType,
        Size:        in.Body.Size,
    })
    if err != nil {
        return nil, err
    }
    return &FileOutput{Body: file}, nil
}

func (a *DirectUpload) SignPut(ctx context.Context, in *FileIDInput) (*SignedOutput, error) {
    owner, _ := identity.FromContext(ctx)
    signed, err := a.files.SignPut(ctx, owner, in.ID)
    if err != nil {
        return nil, err
    }
    return &SignedOutput{Body: signed}, nil
}

func (a *DirectUpload) CreateMultipart(ctx context.Context, in *FileIDInput) (*MultipartOutput, error) {
    owner, _ := identity.FromContext(ctx)
    upload, err := a.files.CreateMultipart(ctx, owner, in.ID)
    if err != nil {
        return nil, err
    }
    return &MultipartOutput{Body: upload}, nil
}

func (a *DirectUpload) SignPart(ctx context.Context, in *SignPartInput) (*SignedOutput, error) {
    owner, _ := identity.FromContext(ctx)
    signed, err := a.files.SignPart(ctx, owner, in.ID, in.UploadID, in.Number)
    if err != nil {
        return nil, err
    }
    return &SignedOutput{Body: signed}, nil
}

func (a *DirectUpload) CompleteMultipart(ctx context.Context, in *CompleteMultipartInput) (*FileOutput, error) {
    owner, _ := identity.FromContext(ctx)
    file, err := a.files.CompleteMultipart(ctx, owner, in.ID, in.UploadID, in.Body.Parts)
    if err != nil {
        return nil, err
    }
    return &FileOutput{Body: file}, nil
}

func (a *DirectUpload) AbortMultipart(ctx context.Context, in *UploadIDInput) (*NoContentOutput, error) {
    owner, _ := identity.FromContext(ctx)
    if err := a.files.AbortMultipart(ctx, owner, in.ID, in.UploadID); err != nil {
        return nil, err
    }
    return &NoContentOutput{}, nil
}
```

浏览器端的完整流程如下。小于 5GiB 的文件可以走单次上传，这里统一用分片演示；`api` 是带上登录 token 调用应用接口、返回 JSON 的函数：

```js
const file = input.files[0]
const record = await api('POST', '/files/prepare', { filename: file.name, content_type: file.type, size: file.size })
const upload = await api('POST', `/files/${record.id}/multipart`)

const parts = []
for (let n = 1; n <= upload.part_count; n++) {
  const chunk = file.slice((n - 1) * upload.part_size, n * upload.part_size)
  const signed = await api('POST', `/files/${record.id}/multipart/${upload.upload_id}/parts/${n}/sign`)
  const res = await fetch(signed.url, { method: signed.method, headers: signed.headers, body: chunk })
  parts.push({ number: n, etag: res.headers.get('ETag') })
}

await api('POST', `/files/${record.id}/multipart/${upload.upload_id}/complete`, { parts })
// 把 record.id 提交给业务接口，例如作为文章封面
```

每一片在上传前才签名，可以并发上传多片；某一片失败时，重新签名并重传这一片即可。

<a id="rest-confirm"></a>
### 在业务接口中确认

客户端上传完成后，把文件 ID 提交给使用它的业务接口，由业务接口确认。例如创建文章时设置封面：

```go
type CreateArticleInput struct {
    Body struct {
        Title   string    `json:"title" minLength:"1" maxLength:"200"`
        CoverID uuid.UUID `json:"cover_id"`
    }
}

func (a *CreateArticle) Handle(ctx context.Context, in *CreateArticleInput) (*ArticleOutput, error) {
    owner, _ := identity.FromContext(ctx)
    cover, err := a.files.Confirm(ctx, owner, in.Body.CoverID)
    if err != nil {
        return nil, err
    }
    created, err := a.articles.Create(ctx, owner, article.CreateInput{
        Title:    in.Body.Title,
        CoverID:  cover.ID,
        CoverURL: cover.URL,
    })
    if err != nil {
        return nil, err
    }
    return newArticleOutput(created), nil
}
```

用户提交了别人的文件 ID、已过期的 ID，或者文件还没上传完时，`Confirm` 会返回错误，接口直接返回它即可。需要确认与业务写入在同一事务中时，在业务服务内部使用 `WithDB(tx)`，见[确认文件](#confirming-files)。

<a id="rest-limits"></a>
### 请求体大小与超时

服务端上传的请求体通常远大于普通接口。[默认中间件](middleware.md#default-middleware)中的 `BodyLimit` 把请求体限制在 1 MiB，`Timeout` 也会限制请求时长，需要为 `/files` 单独放宽，做法见[自行组装中间件](middleware.md#custom-stack)。上限至少应为 `max_upload_size` 加上表单的少量额外开销。Huma 不限制 `multipart/form-data` 请求体的大小，不需要再设置 `Operation.MaxBodyBytes`。

直传接口只收发少量 JSON，使用默认限制即可，文件正文不经过应用服务器。

<a id="rest-errors"></a>
### 错误响应

`filesystem` 的错误是 [errorx](errors.md) 业务错误，处理器原样返回时，会转换为下面的状态码，响应体中的 `code` 字段是稳定的错误码：

| 错误 | 状态码 | `code` |
| --- | --- | --- |
| `filesystem.ErrInvalid` | 422 | `filesystem.invalid` |
| `filesystem.ErrTooLarge` | 422 | `filesystem.too_large` |
| `filesystem.ErrNotFound` | 404 | `filesystem.not_found` |
| `filesystem.ErrNotUploaded` | 409 | `filesystem.not_uploaded` |
| `filesystem.ErrUnsupported` | 422 | `filesystem.direct_upload_unsupported` |

```json
{
  "status": 404,
  "title": "Not Found",
  "detail": "Not Found",
  "code": "filesystem.not_found"
}
```

需要本地化提示时，在翻译文件中以错误码为消息 ID 添加译文，见[本地化](localization.md)。

<a id="cleanup"></a>
## 过期清理

存储中的对象只有一个删除入口：`Cleanup`。它每次最多处理 `limit` 条到期记录，返回成功清理的数量：

- 超过 `temp_ttl` 仍未确认的 `temporary` 文件：中止未完成的分片会话，删除对象和记录；
- 已 `Delete` 的文件：删除对象和记录。

```go
n, err := fileService.Cleanup(ctx, 500)
```

`Cleanup` 被设计为异步队列中的定时任务：每次处理一批，可以反复执行，多个实例同时执行也是安全的。某条记录清理失败时，它会推迟 5 分钟再重试，错误合并返回，不影响其他记录。正在被确认的文件不会被误删：清理和确认通过带条件的更新互斥，只有一方能成功。

> [!NOTE]
> 异步队列功能还在开发中，完成后会提供把 `Cleanup` 注册为定时任务的方式。在此之前，可以在应用自己的定时器或命令中定期调用它，例如每小时一次。

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
| `expires_at` | `temporary` 的回收时间，`deleted` 的清理时间；`ready` 为空 |
| `created_at / updated_at` | 创建和更新时间 |

你可以为 `files` 表增加列，框架只读写上面这些列。所有状态变更都是带条件的单条 `UPDATE`，存储读写不占用数据库事务，多个进程同时操作同一个文件也是安全的。

服务返回的错误可以用 `errors.Is` 匹配，在 REST 接口中的状态码见[错误响应](#rest-errors)：

| 错误 | 含义 |
| --- | --- |
| `filesystem.ErrInvalid` | 输入不合法，例如正文长度与 `Size` 不符、分片编号错误 |
| `filesystem.ErrTooLarge` | 超过 `max_upload_size` |
| `filesystem.ErrNotFound` | 文件不存在、已删除、已过期，或不属于当前主体 |
| `filesystem.ErrNotUploaded` | 确认时对象还没有上传完整 |
| `filesystem.ErrUnsupported` | 磁盘不支持客户端直传 |
