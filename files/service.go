package files

import (
	"context"
	"database/sql"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
)

const (
	maxObjectSize = 5 * TiB          // S3 单对象上限
	retryDelay    = 5 * time.Minute  // 对象清理失败后的重试间隔
	signTTL       = 15 * time.Minute // 直传签名有效期，签完立即使用
)

// Service 管理 files 表和应用装配的磁盘。存储读写不放在数据库事务里，
// 状态变更都是带条件的单条 UPDATE，多进程并发安全。
type Service struct {
	db    bun.IDB
	disks map[string]Disk
	cfg   Config
}

// New 创建文件服务。db 必须已执行 files 迁移；disks 的键是写入 files.disk 的稳定名称。
func New(db *bun.DB, cfg Config, disks map[string]Disk) (*Service, error) {
	if db == nil {
		return nil, errors.New("files: database is required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "files")
	}
	copied := make(map[string]Disk, len(disks))
	for name, disk := range disks {
		if strings.TrimSpace(name) == "" || disk == nil {
			return nil, errors.New("files: disk name and disk are required")
		}
		copied[name] = disk
	}
	if copied[cfg.DefaultDisk] == nil {
		return nil, errors.Newf("files: default disk %q is not configured", cfg.DefaultDisk)
	}
	return &Service{db: db, disks: copied, cfg: cfg}, nil
}

// WithDB 返回使用 db（通常是业务事务）的副本，用于让 Confirm、Delete 与业务数据一起提交。
func (s *Service) WithDB(db bun.IDB) *Service {
	cp := *s
	cp.db = db
	return &cp
}

// Prepare 登记一个 temporary 文件并分配对象键，供客户端直传。超过 TempTTL 未确认会被回收。
func (s *Service) Prepare(ctx context.Context, owner identity.Subject, in FileInput) (Record, error) {
	if owner.ID == "" || owner.Type == "" || in.Size < 0 {
		return Record{}, ErrInvalid
	}
	if in.Size > int64(s.cfg.MaxUploadSize) {
		return Record{}, ErrTooLarge
	}
	if in.Disk == "" {
		in.Disk = s.cfg.DefaultDisk
	}
	if s.disks[in.Disk] == nil {
		return Record{}, errors.Wrapf(ErrInvalid, "unknown disk %q", in.Disk)
	}
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(in.Filename), "\\", "/"))
	if name == "." || name == "/" || name == ".." || strings.ContainsAny(name, "\x00\r\n") {
		return Record{}, errors.Wrap(ErrInvalid, "invalid filename")
	}
	contentType := strings.TrimSpace(in.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if strings.ContainsAny(contentType, "\x00\r\n") {
		return Record{}, errors.Wrap(ErrInvalid, "invalid content type")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	expires := now.Add(s.cfg.TempTTL)
	r := Record{
		ID: id, Disk: in.Disk, Key: objectKey(id, now, name), Filename: name, ContentType: contentType, Size: in.Size,
		OwnerType: string(owner.Type), OwnerID: owner.ID, Status: StatusTemporary, ExpiresAt: &expires,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.db.NewInsert().Model(&r).Exec(ctx); err != nil {
		return Record{}, errors.Wrap(err, "files: prepare")
	}
	return s.withURL(r), nil
}

// Upload 由应用服务器接收正文并写入磁盘，返回 temporary 记录；业务使用时再 Confirm。
// body 长度必须正好等于 in.Size。
func (s *Service) Upload(ctx context.Context, owner identity.Subject, in FileInput, body io.Reader) (Record, error) {
	if body == nil {
		return Record{}, ErrInvalid
	}
	r, err := s.Prepare(ctx, owner, in)
	if err != nil {
		return Record{}, err
	}
	if err := s.disks[r.Disk].Put(ctx, r.Key, r.ContentType, &exactReader{r: body, left: r.Size}, r.Size); err != nil {
		// 磁盘保证失败时不发布对象，记录直接删除；删除失败也会在过期后被回收。
		_, _ = s.db.NewDelete().Model((*Record)(nil)).Where("id = ?", r.ID).Exec(context.WithoutCancel(ctx))
		return Record{}, errors.Wrap(err, "files: upload")
	}
	return r, nil
}

// Confirm 核对对象已完整上传，并把 temporary 文件转为 ready。重复确认返回原记录。
// 只有上传者本人可以确认，避免引用他人上传的临时文件。
func (s *Service) Confirm(ctx context.Context, owner identity.Subject, id uuid.UUID) (Record, error) {
	r, err := s.owned(ctx, owner, id)
	if err != nil || r.Status == StatusReady {
		return r, err
	}
	if err := s.checkUploaded(ctx, r); err != nil {
		return Record{}, err
	}
	now := time.Now().UTC()
	res, err := s.db.NewUpdate().Model((*Record)(nil)).
		Set("status = ?", StatusReady).Set("expires_at = NULL").Set("updated_at = ?", now).
		Where("id = ?", id).Where("status = ?", StatusTemporary).Where("expires_at > ?", now).Exec(ctx)
	if err != nil {
		return Record{}, errors.Wrap(err, "files: confirm")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// 并发确认已成功时返回最新记录；被删除或刚好过期则视为不存在。
		r, err = s.owned(ctx, owner, id)
		if err == nil && r.Status != StatusReady {
			err = ErrNotFound
		}
		return r, err
	}
	r.Status, r.ExpiresAt, r.UpdatedAt = StatusReady, nil, now
	return r, nil
}

// Get 返回未删除的文件记录。服务不做业务授权，调用前由应用检查访问权限。
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Record, error) {
	var r Record
	err := s.db.NewSelect().Model(&r).Where("f.id = ?", id).Where("f.status <> ?", StatusDeleted).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, errors.Wrap(err, "files: get")
	}
	return s.withURL(r), nil
}

// Open 打开文件正文，供服务端二次处理；对外访问请使用 Record.URL。调用方负责关闭。
func (s *Service) Open(ctx context.Context, id uuid.UUID) (io.ReadCloser, Record, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return nil, Record{}, err
	}
	body, err := s.disk(r).Open(ctx, r.Key)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, Record{}, ErrNotFound
	}
	return body, r, err
}

// Delete 把文件标为 deleted 并删除对象和记录；文件不存在视为成功。
// 在事务中（WithDB(tx)）只标记删除，提交后由 Cleanup 删除对象，事务回滚则文件保留。
// 对象删除失败时返回错误，记录保持 deleted，由 Cleanup 重试。
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	var r Record
	now := time.Now().UTC()
	err := s.db.NewUpdate().Model(&r).
		Set("status = ?", StatusDeleted).Set("expires_at = ?", now).Set("updated_at = ?", now).
		Where("id = ?", id).Returning("*").Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "files: delete")
	}
	switch s.db.(type) {
	case bun.Tx, *bun.Tx:
		return nil
	}
	return s.purge(ctx, r)
}

// Cleanup 回收最多 limit 条到期记录：过期未确认的 temporary 文件和对象清理失败的 deleted 文件。
// 返回成功清理的数量；失败的记录推迟重试并合并返回错误。应用用调度器或命令定期调用。
func (s *Service) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, ErrInvalid
	}
	var rows []Record
	now := time.Now().UTC()
	if err := s.db.NewSelect().Model(&rows).Where("f.expires_at <= ?", now).
		OrderExpr("f.expires_at").Limit(int64(limit)).Scan(ctx); err != nil {
		return 0, errors.Wrap(err, "files: cleanup")
	}
	done := 0
	var errs []error
	for _, r := range rows {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		if r.Status == StatusTemporary {
			// 只回收仍未确认且已过期的记录，与并发 Confirm 互斥。
			res, err := s.db.NewUpdate().Model((*Record)(nil)).
				Set("status = ?", StatusDeleted).Set("updated_at = ?", now).
				Where("id = ?", r.ID).Where("status = ?", StatusTemporary).Where("expires_at <= ?", now).Exec(ctx)
			if err != nil {
				errs = append(errs, errors.Wrapf(err, "cleanup %s", r.ID))
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
				continue
			}
		}
		if err := s.purge(ctx, r); err != nil {
			errs = append(errs, errors.Wrapf(err, "cleanup %s", r.ID))
			_, _ = s.db.NewUpdate().Model((*Record)(nil)).Set("expires_at = ?", now.Add(retryDelay)).
				Where("id = ?", r.ID).Where("status = ?", StatusDeleted).Exec(context.WithoutCancel(ctx))
			continue
		}
		done++
	}
	return done, errors.Join(errs...)
}

// purge 删除 deleted 记录的对象和未完成分片会话，再删除记录。
func (s *Service) purge(ctx context.Context, r Record) error {
	disk := s.disks[r.Disk]
	if disk == nil {
		return errors.Newf("files: disk %q is not configured", r.Disk)
	}
	if direct, ok := disk.(DirectUploader); ok {
		if err := direct.AbortAllMultipart(ctx, r.Key); err != nil {
			return err
		}
	}
	if err := disk.Delete(ctx, r.Key); err != nil {
		return err
	}
	_, err := s.db.NewDelete().Model((*Record)(nil)).Where("id = ?", r.ID).Where("status = ?", StatusDeleted).Exec(ctx)
	return err
}

// owned 返回 owner 上传的可用文件：ready，或尚未过期的 temporary。
func (s *Service) owned(ctx context.Context, owner identity.Subject, id uuid.UUID) (Record, error) {
	if owner.ID == "" || owner.Type == "" {
		return Record{}, ErrInvalid
	}
	r, err := s.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	if r.OwnerID != owner.ID || r.OwnerType != string(owner.Type) ||
		(r.Status == StatusTemporary && (r.ExpiresAt == nil || !r.ExpiresAt.After(time.Now()))) {
		return Record{}, ErrNotFound
	}
	return r, nil
}

// checkUploaded 核对对象存在且实际大小等于声明大小。
func (s *Service) checkUploaded(ctx context.Context, r Record) error {
	info, err := s.disk(r).Stat(ctx, r.Key)
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotUploaded
	}
	if err != nil {
		return errors.Wrap(err, "files: stat")
	}
	if info.Size != r.Size {
		return ErrNotUploaded
	}
	return nil
}

func (s *Service) disk(r Record) Disk {
	if d := s.disks[r.Disk]; d != nil {
		return d
	}
	return missingDisk(r.Disk)
}

func (s *Service) withURL(r Record) Record {
	if d := s.disks[r.Disk]; d != nil {
		r.URL = d.URL(r.Key)
	}
	return r
}

// objectKey 按上传日期分目录，文件名用 ID 加小写扩展名，公开地址不暴露原始文件名。
func objectKey(id uuid.UUID, now time.Time, filename string) string {
	return now.Format("2006/01/02/") + id.String() + extension(filename)
}

// extension 取小写扩展名，只保留 [a-z0-9] 且不超过 16 个字符，否则返回空串。
func extension(filename string) string {
	ext := strings.ToLower(path.Ext(filename))
	if len(ext) < 2 || len(ext) > 17 {
		return ""
	}
	for _, c := range ext[1:] {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ""
		}
	}
	return ext
}

// exactReader 只放行 left 字节；正文过短或过长时返回错误，使磁盘放弃发布。
type exactReader struct {
	r    io.Reader
	left int64
}

func (e *exactReader) Read(p []byte) (int, error) {
	if e.left == 0 {
		var one [1]byte
		n, err := e.r.Read(one[:])
		if n > 0 {
			return 0, errors.Wrap(ErrInvalid, "body longer than declared size")
		}
		if err == nil {
			return 0, nil
		}
		return 0, err
	}
	if int64(len(p)) > e.left {
		p = p[:e.left]
	}
	n, err := e.r.Read(p)
	e.left -= int64(n)
	if errors.Is(err, io.EOF) && e.left > 0 {
		return n, errors.Wrap(ErrInvalid, "body shorter than declared size")
	}
	if errors.Is(err, io.EOF) {
		err = nil
	}
	return n, err
}

type missingDisk string

func (m missingDisk) err() error { return errors.Newf("files: disk %q is not configured", string(m)) }
func (m missingDisk) Put(context.Context, string, string, io.Reader, int64) error {
	return m.err()
}
func (m missingDisk) Open(context.Context, string) (io.ReadCloser, error) { return nil, m.err() }
func (m missingDisk) Stat(context.Context, string) (Object, error)        { return Object{}, m.err() }
func (m missingDisk) Delete(context.Context, string) error                { return m.err() }
func (m missingDisk) URL(string) string                                   { return "" }
