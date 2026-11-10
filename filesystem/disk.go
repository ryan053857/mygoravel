// Package filesystem 提供依赖 files 表的文件上传、确认、公开访问和清理服务。
package filesystem

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/cockroachdb/errors"
)

var (
	ErrNotFound    = errors.New("filesystem: file not found")
	ErrInvalid     = errors.New("filesystem: invalid input")
	ErrTooLarge    = errors.New("filesystem: file too large")
	ErrNotUploaded = errors.New("filesystem: object missing or size differs")
	ErrUnsupported = errors.New("filesystem: disk does not support direct upload")
)

// Object 是存储中实际对象的信息。
type Object struct {
	Size int64
}

// Disk 是一个可公开访问的存储位置。对象不存在时 Open、Stat 返回可匹配 io/fs.ErrNotExist 的错误；
// Delete 幂等。Put 必须原子发布：body 返回错误时不得留下部分对象。
type Disk interface {
	Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error
	Open(ctx context.Context, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (Object, error)
	Delete(ctx context.Context, key string) error
	// URL 返回对象的公开访问地址，即公开基址拼接对象键。
	URL(key string) string
}

// Part 是客户端直传完成的分片，ETag 取自上传响应头，原样回传。
type Part struct {
	Number int32  `json:"number"`
	ETag   string `json:"etag"`
}

// SignedRequest 是客户端直传时使用的地址、方法和必须携带的请求头。
type SignedRequest struct {
	URL     string      `json:"url"`
	Method  string      `json:"method"`
	Headers http.Header `json:"headers,omitempty"`
}

// DirectUploader 是支持客户端直传的磁盘（如 S3）额外提供的能力。
// 签名绑定内容长度；AbortMultipart 幂等，AbortAllMultipart 中止某个对象键上的所有分片会话。
type DirectUploader interface {
	SignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (SignedRequest, error)
	CreateMultipart(ctx context.Context, key, contentType string) (uploadID string, err error)
	SignPart(ctx context.Context, key, uploadID string, number int32, size int64, ttl time.Duration) (SignedRequest, error)
	CompleteMultipart(ctx context.Context, key, uploadID string, parts []Part) error
	AbortMultipart(ctx context.Context, key, uploadID string) error
	AbortAllMultipart(ctx context.Context, key string) error
}
