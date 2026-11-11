// Package exports 在后台把数据导出为表格：记录进度，完成后把文件放到文件存储，交给用户下载。
//
// 一次导出是一个 operation：状态、进度和失败的原因记在 operations 表中，与导入、批量处理出现在同一个任务列表里；
// exports 表只记录格式和生成的文件。
//
// Start 在事务中登记导出，同一个事务发布队列任务；任务调用 Service.Process，业务代码把行写入 Writer：
//
//	exp, err := s.Start(ctx, tx, subject, "articles.export", sheet.XLSX)
//	err = ExportArticles.Publish(ctx, queue.Database(tx), exp.ID)
//
//	// 队列任务
//	err := s.Process(ctx, id, func(ctx context.Context, exp *exports.Export, w *exports.Writer) error {
//		return export.Handle(ctx, subject, w, columns)
//	}, nil)
//
// 文件以 temporary 状态上传，超过 filesystem 的 temp_ttl 后被回收；Export.URL 是它的公开地址。
// 进度由 Get 查询，operation 的 Cleanup 删除旧的导出。
package exports

import (
	"context"
	"database/sql"
	"io"
	"os"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/sheet"
)

// Export 是一次后台导出：嵌入的 Operation 是它的状态和进度，一行是一个单位；
// 其余字段是 exports 表的 Bun 模型。
type Export struct {
	bun.BaseModel       `bun:"table:exports,alias:ex"`
	operation.Operation `bun:"-"`

	OperationID uuid.UUID    `bun:"operation_id,pk,type:uuid" json:"-"`
	Format      sheet.Format `bun:"format,notnull"            json:"format"`
	// FileID、Filename、URL 和 ExpiresAt 在 completed 时写入：上传的文件、它的公开地址和被回收的时间。
	FileID    uuid.UUID  `bun:"file_id,type:uuid,nullzero" json:"file_id,omitzero"`
	Filename  string     `bun:"filename,notnull"           json:"filename,omitempty"`
	URL       string     `bun:"url,notnull"                json:"url,omitempty"`
	ExpiresAt *time.Time `bun:"expires_at"                 json:"expires_at,omitempty"`
}

// Service 登记、处理和查询导出。
type Service struct {
	db    *bun.DB
	ops   *operation.Service
	files *filesystem.Service
}

// New 返回导出服务。ops 记录导出的状态和进度，files 保存导出的文件。
func New(db *bun.DB, ops *operation.Service, files *filesystem.Service) *Service {
	return &Service{db: db, ops: ops, files: files}
}

// Start 登记 owner 发起的一次 kind 导出（例如 articles.export），返回 pending 状态的导出。
// format 不是 CSV 或 XLSX 时返回 sheet.ErrFormat。
// tx 通常是发布导出任务的事务，在同一个事务中用 queue.Database(tx) 发布任务，任务中调用 Process。
func (s *Service) Start(ctx context.Context, tx bun.IDB, owner identity.Subject, kind string, format sheet.Format) (*Export, error) {
	if format != sheet.CSV && format != sheet.XLSX {
		return nil, sheet.ErrFormat.New()
	}
	op, err := s.ops.Start(ctx, tx, owner, kind)
	if err != nil {
		return nil, err
	}
	exp := &Export{Operation: *op, OperationID: op.ID, Format: format}
	_, err = tx.NewInsert().Model(exp).Exec(ctx)
	return exp, errors.Wrap(err, "exports: start")
}

// Get 返回 owner 发起的导出；不存在、不是导出或者不是 owner 发起的时返回 operation.ErrNotFound。
func (s *Service) Get(ctx context.Context, owner identity.Subject, id uuid.UUID) (*Export, error) {
	op, err := s.ops.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	exp, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if exp == nil {
		return nil, operation.ErrNotFound.New()
	}
	exp.Operation = *op
	return exp, nil
}

// load 读取导出的格式和文件，不存在时返回 nil。
func (s *Service) load(ctx context.Context, id uuid.UUID) (*Export, error) {
	exp := new(Export)
	err := s.db.NewSelect().Model(exp).Where("operation_id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "exports: load")
	}
	return exp, nil
}

// saveInterval 是两次保存进度之间的最短时间。
var saveInterval = time.Second

// Writer 写出导出的表格，并记录进度：每秒最多保存一次已经写出的行数。第一行是表头，不计入行数。
type Writer struct {
	ctx    context.Context
	w      *sheet.Writer
	r      *operation.Run
	header bool
	last   time.Time
}

// Write 写出一行。
func (w *Writer) Write(cells ...any) error {
	if err := w.w.Write(cells...); err != nil {
		return err
	}
	if !w.header {
		w.header = true
		return nil
	}
	w.r.Add(1, 0)
	if time.Since(w.last) < saveInterval {
		return nil
	}
	w.last = time.Now()
	return w.r.Save(w.ctx, nil)
}

// SetTotal 设置预计的行数并立即保存，前端据此显示进度百分比。不调用时 Total 为 0。
func (w *Writer) SetTotal(n int) error {
	w.r.SetTotal(n)
	return w.r.Save(w.ctx, nil)
}

// Process 处理导出 id，在队列任务中调用。它用 operation.Service.Run 执行导出，一行是一个单位。
//
// write 把行写入 w，第一行是表头；写入的内容先保存在本地临时文件中，
// write 返回后以 temporary 状态上传到文件存储，导出变为 completed，URL 和 ExpiresAt 是文件的公开地址和回收时间。
// 文件名是 kind 中的点换成连字符，加上日期和扩展名，例如 articles-export-20261111.xlsx。
//
// 每次保存进度（变为 running、SetTotal、写出过程中每秒最多一次、结束）都在同一个事务中调用 saved（可以为 nil），
// exp 是保存后的导出，用法见 operation.Service.Run。
//
// write 返回带错误码的业务错误（例如 sheet.ErrUnknownColumn）时导出变为 failed，Error 是错误码；
// 其他错误照常返回给队列，重试时从头重新导出。队列的最后一次执行仍然出错时导出变为 failed，Error 是 operation.failed。
// 导出不存在（例如已被清理）或者已经结束时，Process 什么也不做，返回 nil。
func (s *Service) Process(ctx context.Context, id uuid.UUID,
	write func(ctx context.Context, exp *Export, w *Writer) error,
	saved func(ctx context.Context, tx database.Tx, exp *Export) error,
) error {
	exp, err := s.load(ctx, id)
	if err != nil || exp == nil {
		return err
	}
	return s.ops.Run(ctx, id, func(ctx context.Context, r *operation.Run) error {
		exp.Operation = *r.Operation()
		r.Restart() // 重试时从头开始
		return s.export(ctx, r, exp, write)
	}, func(ctx context.Context, tx database.Tx, op *operation.Operation) error {
		exp.Operation = *op
		if saved == nil {
			return nil
		}
		return saved(ctx, tx, exp)
	})
}

func (s *Service) export(ctx context.Context, r *operation.Run, exp *Export, write func(ctx context.Context, exp *Export, w *Writer) error) error {
	tmp, err := os.CreateTemp("", "export-*."+string(exp.Format))
	if err != nil {
		return errors.Wrap(err, "exports: create temporary file")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	w := &Writer{ctx: ctx, w: sheet.NewWriter(tmp, exp.Format), r: r, last: time.Now()}
	if err := errors.CombineErrors(write(ctx, exp, w), w.w.Close()); err != nil {
		return err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		return errors.Wrap(err, "exports: rewind temporary file")
	}
	file, err := s.files.Upload(ctx, exp.Owner(), filesystem.FileInput{
		Filename:    strings.ReplaceAll(exp.Kind, ".", "-") + "-" + time.Now().Format("20060102") + "." + string(exp.Format),
		ContentType: exp.Format.ContentType(),
		Size:        size,
	}, tmp)
	if err != nil {
		return err
	}
	exp.FileID, exp.Filename, exp.URL, exp.ExpiresAt = file.ID, file.Filename, file.URL, file.ExpiresAt
	return r.Complete(ctx, func(ctx context.Context, tx database.Tx) error {
		_, err := tx.NewUpdate().Model(exp).Column("file_id", "filename", "url", "expires_at").WherePK().Exec(ctx)
		return errors.Wrap(err, "exports: save file")
	})
}
