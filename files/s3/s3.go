// Package s3 把文件保存在 S3 兼容存储中，通过公开基址访问，并支持客户端直传。
package s3

import (
	"bytes"
	"context"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/files"
)

// uploadPartSize 是服务端 Put 的分片缓冲大小，大于它的正文按分片上传，内存占用不超过一片。
const uploadPartSize = 16 << 20

// Config 是 S3 磁盘配置。未提供静态密钥时使用 AWS 默认凭证链。
type Config struct {
	Bucket    string `yaml:"bucket"`
	Region    string `yaml:"region"`
	Endpoint  string `yaml:"endpoint"`   // S3 兼容服务的 API 地址，AWS 留空
	PathStyle bool   `yaml:"path_style"` // 使用 path-style 访问桶，MinIO 等通常需要
	AccessKey string `yaml:"access_key"`
	SecretKey string `yaml:"secret_key"`
	PublicURL string `yaml:"public_url"` // 公开访问基址（CDN 或桶域名），必填
}

// Validate 检查桶、公开基址、端点和静态凭证。
func (c Config) Validate() error {
	var errs []error
	if strings.TrimSpace(c.Bucket) == "" {
		errs = append(errs, errors.New("bucket is required"))
	}
	if !httpURL(c.PublicURL) {
		errs = append(errs, errors.New("public_url must be an http(s) URL"))
	}
	if c.Endpoint != "" && !httpURL(c.Endpoint) {
		errs = append(errs, errors.New("endpoint must be an http(s) URL"))
	}
	if (c.AccessKey == "") != (c.SecretKey == "") {
		errs = append(errs, errors.New("access_key and secret_key must be set together"))
	}
	return errors.Join(errs...)
}

// Disk 是 S3 磁盘，实现 files.Disk 和 files.DirectUploader。
type Disk struct {
	client    *awss3.Client
	presigner *awss3.PresignClient
	bucket    string
	baseURL   string
}

var (
	_ files.Disk           = (*Disk)(nil)
	_ files.DirectUploader = (*Disk)(nil)
)

// Open 按配置创建客户端并检查桶可访问。桶和公开访问策略由运维预先配置。
func Open(ctx context.Context, cfg Config) (*Disk, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "s3")
	}
	var opts []func(*awsconfig.LoadOptions) error
	if cfg.Region != "" {
		opts = append(opts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKey != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, "")))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "s3: load config")
	}
	if awsCfg.Region == "" {
		return nil, errors.New("s3: region is required")
	}
	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		o.UsePathStyle = cfg.PathStyle
		// 只在必需时计算校验和，预签名请求不要求客户端额外携带校验和头。
		o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	})
	if _, err := client.HeadBucket(ctx, &awss3.HeadBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		return nil, errors.Wrap(err, "s3: head bucket")
	}
	return New(client, cfg.Bucket, cfg.PublicURL)
}

// New 使用应用自己创建的客户端，不访问网络。
func New(client *awss3.Client, bucket, publicURL string) (*Disk, error) {
	if client == nil || strings.TrimSpace(bucket) == "" || !httpURL(publicURL) {
		return nil, errors.New("s3: client, bucket and http(s) public URL are required")
	}
	return &Disk{client: client, presigner: awss3.NewPresignClient(client), bucket: bucket, baseURL: strings.TrimRight(publicURL, "/")}, nil
}

// Client 返回原生客户端，供应用使用未封装的 S3 能力。
func (d *Disk) Client() *awss3.Client { return d.client }

// URL 返回对象的公开地址。
func (d *Disk) URL(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return d.baseURL + "/" + strings.Join(parts, "/")
}

// Put 流式上传：不超过一片时单次 PUT，否则逐片缓冲上传，内存占用不超过一片。
// 读取正文失败时中止分片会话，不发布部分对象。
func (d *Disk) Put(ctx context.Context, key, contentType string, body io.Reader, size int64) error {
	buf := make([]byte, min(size, uploadPartSize)+1)
	n, err := io.ReadFull(body, buf)
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		if err != nil {
			return err
		}
		return d.putMultipart(ctx, key, contentType, buf[:n], body)
	}
	_, err = d.client.PutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key), Body: bytes.NewReader(buf[:n]),
		ContentLength: aws.Int64(int64(n)), ContentType: optional(contentType),
	})
	return wrap("put object", err)
}

func (d *Disk) putMultipart(ctx context.Context, key, contentType string, first []byte, rest io.Reader) (err error) {
	uploadID, err := d.CreateMultipart(ctx, key, contentType)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, d.AbortMultipart(context.WithoutCancel(ctx), key, uploadID))
		}
	}()
	// first 比一片多读了一个字节，把它接回剩余正文。
	body := io.MultiReader(bytes.NewReader(first[uploadPartSize:]), rest)
	part := first[:uploadPartSize]
	var parts []files.Part
	for number := int32(1); ; number++ {
		out, err := d.client.UploadPart(ctx, &awss3.UploadPartInput{
			Bucket: aws.String(d.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
			PartNumber: aws.Int32(number), Body: bytes.NewReader(part), ContentLength: aws.Int64(int64(len(part))),
		})
		if err != nil {
			return wrap("upload part", err)
		}
		parts = append(parts, files.Part{Number: number, ETag: aws.ToString(out.ETag)})
		next := make([]byte, uploadPartSize)
		n, err := io.ReadFull(body, next)
		if n == 0 && errors.Is(err, io.EOF) {
			break
		}
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
			return err
		}
		part = next[:n]
	}
	return d.CompleteMultipart(ctx, key, uploadID, parts)
}

// Open 返回对象正文，调用方负责关闭。
func (d *Disk) Open(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := d.client.GetObject(ctx, &awss3.GetObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(key)})
	if err != nil {
		return nil, wrap("get object", err)
	}
	return out.Body, nil
}

// Stat 返回对象大小。
func (d *Disk) Stat(ctx context.Context, key string) (files.Object, error) {
	out, err := d.client.HeadObject(ctx, &awss3.HeadObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(key)})
	if err != nil {
		return files.Object{}, wrap("head object", err)
	}
	return files.Object{Size: aws.ToInt64(out.ContentLength)}, nil
}

// Delete 删除对象，不存在视为成功。
func (d *Disk) Delete(ctx context.Context, key string) error {
	_, err := d.client.DeleteObject(ctx, &awss3.DeleteObjectInput{Bucket: aws.String(d.bucket), Key: aws.String(key)})
	if errors.Is(wrap("", err), fs.ErrNotExist) {
		return nil
	}
	return wrap("delete object", err)
}

// SignPut 签发单次上传地址，绑定长度和类型。
func (d *Disk) SignPut(ctx context.Context, key, contentType string, size int64, ttl time.Duration) (files.SignedRequest, error) {
	out, err := d.presigner.PresignPutObject(ctx, &awss3.PutObjectInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key), ContentType: optional(contentType), ContentLength: aws.Int64(size),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return files.SignedRequest{}, wrap("presign put", err)
	}
	return signed(out.URL, out.Method, out.SignedHeader), nil
}

// CreateMultipart 创建分片会话。
func (d *Disk) CreateMultipart(ctx context.Context, key, contentType string) (string, error) {
	out, err := d.client.CreateMultipartUpload(ctx, &awss3.CreateMultipartUploadInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key), ContentType: optional(contentType),
	})
	if err != nil {
		return "", wrap("create multipart", err)
	}
	if aws.ToString(out.UploadId) == "" {
		return "", errors.New("s3: create multipart returned no upload ID")
	}
	return *out.UploadId, nil
}

// SignPart 签发单片上传地址，绑定会话、编号和长度。
func (d *Disk) SignPart(ctx context.Context, key, uploadID string, number int32, size int64, ttl time.Duration) (files.SignedRequest, error) {
	out, err := d.presigner.PresignUploadPart(ctx, &awss3.UploadPartInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		PartNumber: aws.Int32(number), ContentLength: aws.Int64(size),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return files.SignedRequest{}, wrap("presign part", err)
	}
	return signed(out.URL, out.Method, out.SignedHeader), nil
}

// CompleteMultipart 合并分片；ETag 和分片大小由 S3 校验。
func (d *Disk) CompleteMultipart(ctx context.Context, key, uploadID string, parts []files.Part) error {
	completed := make([]types.CompletedPart, len(parts))
	for i, p := range parts {
		completed[i] = types.CompletedPart{PartNumber: aws.Int32(p.Number), ETag: aws.String(p.ETag)}
	}
	_, err := d.client.CompleteMultipartUpload(ctx, &awss3.CompleteMultipartUploadInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
		MultipartUpload: &types.CompletedMultipartUpload{Parts: completed},
	})
	return wrap("complete multipart", err)
}

// AbortMultipart 中止分片会话，会话不存在视为成功。
func (d *Disk) AbortMultipart(ctx context.Context, key, uploadID string) error {
	_, err := d.client.AbortMultipartUpload(ctx, &awss3.AbortMultipartUploadInput{
		Bucket: aws.String(d.bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
	if errors.Is(wrap("", err), fs.ErrNotExist) {
		return nil
	}
	return wrap("abort multipart", err)
}

// AbortAllMultipart 中止 key 上所有未完成的分片会话。
func (d *Disk) AbortAllMultipart(ctx context.Context, key string) error {
	in := &awss3.ListMultipartUploadsInput{Bucket: aws.String(d.bucket), Prefix: aws.String(key)}
	for {
		out, err := d.client.ListMultipartUploads(ctx, in)
		if err != nil {
			if errors.Is(wrap("", err), fs.ErrNotExist) {
				return nil
			}
			return wrap("list multipart uploads", err)
		}
		for _, u := range out.Uploads {
			if aws.ToString(u.Key) != key {
				continue
			}
			if err := d.AbortMultipart(ctx, key, aws.ToString(u.UploadId)); err != nil {
				return err
			}
		}
		if !aws.ToBool(out.IsTruncated) {
			return nil
		}
		in.KeyMarker, in.UploadIdMarker = out.NextKeyMarker, out.NextUploadIdMarker
	}
}

func signed(rawURL, method string, headers http.Header) files.SignedRequest {
	h := headers.Clone()
	// Host 和 Content-Length 由 HTTP 客户端自动设置，浏览器也不允许手工设置。
	h.Del("Host")
	h.Del("Content-Length")
	return files.SignedRequest{URL: rawURL, Method: method, Headers: h}
}

// wrap 加上操作名，并让对象或会话不存在的错误匹配 fs.ErrNotExist。
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "NoSuchUpload", "404":
			err = errors.Join(fs.ErrNotExist, err)
		}
	}
	if op == "" {
		return err
	}
	return errors.Wrapf(err, "s3: %s", op)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return aws.String(s)
}

func httpURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" && u.RawQuery == "" && u.Fragment == ""
}
