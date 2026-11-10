package filesystem

import (
	"context"
	"slices"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/identity"
)

const (
	maxSinglePut = 5 * GiB  // 单次 PUT 上限，更大的文件使用分片
	minPartSize  = 16 * MiB // 默认分片大小；超大文件自动放大以不超过 maxParts
	maxParts     = 10000
)

// SignPut 为单次直传签名，绑定声明的大小和类型。客户端上传后调用 Confirm。
func (s *Service) SignPut(ctx context.Context, owner identity.Subject, id uuid.UUID) (SignedRequest, error) {
	r, direct, err := s.direct(ctx, owner, id)
	if err != nil {
		return SignedRequest{}, err
	}
	if r.Size > int64(maxSinglePut) {
		return SignedRequest{}, errors.Wrap(ErrInvalid, "file larger than 5GiB must use multipart upload")
	}
	return direct.SignPut(ctx, r.Key, r.ContentType, r.Size, ttl(r))
}

// CreateMultipart 创建分片会话。会话 ID 不入库，由客户端保存并在后续调用中带回。
func (s *Service) CreateMultipart(ctx context.Context, owner identity.Subject, id uuid.UUID) (Multipart, error) {
	r, direct, err := s.direct(ctx, owner, id)
	if err != nil {
		return Multipart{}, err
	}
	if r.Size == 0 {
		return Multipart{}, errors.Wrap(ErrInvalid, "empty file must use SignPut")
	}
	uploadID, err := direct.CreateMultipart(ctx, r.Key, r.ContentType)
	if err != nil {
		return Multipart{}, err
	}
	size := PartSize(r.Size)
	return Multipart{UploadID: uploadID, PartSize: size, PartCount: int((r.Size + size - 1) / size)}, nil
}

// SignPart 为第 number 片签名，绑定该片应有的长度。每片上传前调用，签名立即使用不会过期。
func (s *Service) SignPart(ctx context.Context, owner identity.Subject, id uuid.UUID, uploadID string, number int32) (SignedRequest, error) {
	r, direct, err := s.direct(ctx, owner, id)
	if err != nil {
		return SignedRequest{}, err
	}
	size, count := PartSize(r.Size), partCount(r.Size)
	if uploadID == "" || number < 1 || int(number) > count {
		return SignedRequest{}, errors.Wrap(ErrInvalid, "invalid upload ID or part number")
	}
	length := min(size, r.Size-int64(number-1)*size)
	return direct.SignPart(ctx, r.Key, uploadID, number, length, ttl(r))
}

// CompleteMultipart 按编号合并全部分片。parts 必须正好覆盖 1..PartCount，ETag 取自各片上传响应。
// 合并成功后重试也返回成功。完成后调用 Confirm。
func (s *Service) CompleteMultipart(ctx context.Context, owner identity.Subject, id uuid.UUID, uploadID string, parts []Part) (File, error) {
	r, direct, err := s.direct(ctx, owner, id)
	if err != nil {
		return File{}, err
	}
	parts = slices.Clone(parts)
	slices.SortFunc(parts, func(a, b Part) int { return int(a.Number) - int(b.Number) })
	if uploadID == "" || len(parts) != partCount(r.Size) {
		return File{}, errors.Wrap(ErrInvalid, "incorrect upload ID or part count")
	}
	for i, p := range parts {
		if p.Number != int32(i+1) || p.ETag == "" {
			return File{}, errors.Wrap(ErrInvalid, "parts must be numbered 1..n with ETags")
		}
	}
	if err := direct.CompleteMultipart(ctx, r.Key, uploadID, parts); err != nil {
		// 上次合并已成功但响应丢失时，会话已不存在而对象完整，视为成功。
		if s.checkUploaded(ctx, r) == nil {
			return r, nil
		}
		return File{}, err
	}
	return r, nil
}

// AbortMultipart 中止分片会话，临时文件保留，可以重新创建会话。
func (s *Service) AbortMultipart(ctx context.Context, owner identity.Subject, id uuid.UUID, uploadID string) error {
	r, direct, err := s.direct(ctx, owner, id)
	if err != nil {
		return err
	}
	if uploadID == "" {
		return ErrInvalid
	}
	return direct.AbortMultipart(ctx, r.Key, uploadID)
}

// PartSize 返回 size 字节文件的分片大小：默认 16MiB，超大文件按 MiB 向上取整放大，保证不超过 10000 片。
func PartSize(size int64) int64 {
	part := (size + maxParts - 1) / maxParts
	part = (part + int64(MiB) - 1) / int64(MiB) * int64(MiB)
	return max(int64(minPartSize), part)
}

func partCount(size int64) int {
	part := PartSize(size)
	return int((size + part - 1) / part)
}

// direct 返回 owner 尚未确认的临时文件及其磁盘的直传能力。
func (s *Service) direct(ctx context.Context, owner identity.Subject, id uuid.UUID) (File, DirectUploader, error) {
	r, err := s.owned(ctx, owner, id)
	if err != nil {
		return File{}, nil, err
	}
	if r.Status != StatusTemporary {
		return File{}, nil, errors.Wrap(ErrInvalid, "file is already confirmed")
	}
	direct, ok := s.disk(r).(DirectUploader)
	if !ok {
		return File{}, nil, ErrUnsupported
	}
	return r, direct, nil
}

// ttl 是签名有效期，不超过临时文件的剩余时间。
func ttl(r File) time.Duration {
	return max(time.Second, min(signTTL, time.Until(*r.ExpiresAt).Truncate(time.Second)))
}
