// Package exports 在后台把数据导出为表格：记录进度，完成后把文件放到文件存储，交给用户下载。
//
// Start 在事务中登记导出，同一个事务发布队列任务；任务调用 Service.Process，业务代码把行写入 Writer：
//
//	exp, err := s.Start(ctx, tx, subject, "articles", sheet.XLSX)
//	err = ExportArticles.Publish(ctx, queue.Database(tx), exp.ID)
//
//	// 队列任务
//	err := s.Process(ctx, id, func(ctx context.Context, exp *exports.Export, w *exports.Writer) error {
//		return export.Handle(ctx, subject, w, columns)
//	}, nil)
//
// 文件以 temporary 状态上传，超过 filesystem 的 temp_ttl 后被回收；Export.URL 是它的公开地址。
// 进度由 Get 查询，Cleanup 删除旧的导出记录。
package exports

import (
	"context"
	"database/sql"
	"io"
	"os"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/sheet"
)

// ErrNotFound 表示导出不存在，或者不是这个主体发起的。
var ErrNotFound = errorx.Define(errorx.NotFound, "exports.not_found")

// Status 是导出的状态。
type Status string

const (
	StatusPending   Status = "pending"   // 已登记，等待任务处理
	StatusRunning   Status = "running"   // 正在写出
	StatusCompleted Status = "completed" // 文件已经上传，见 URL
	StatusFailed    Status = "failed"    // 导出出错，或者任务重试用完；原因见 Error
)

// Export 是一次后台导出，也是 exports 表的 Bun 模型。
type Export struct {
	bun.BaseModel `bun:"table:exports,alias:ex"`

	ID        uuid.UUID            `bun:"id,pk,type:uuid"    json:"id"` // UUIDv7，按时间递增
	Tenant    string               `bun:"tenant,notnull"     json:"-"`
	OwnerType identity.SubjectType `bun:"owner_type,notnull" json:"-"`
	OwnerID   string               `bun:"owner_id,notnull"   json:"-"`
	Kind      string               `bun:"kind,notnull"       json:"kind"` // 导出的是什么，由应用定义，例如 articles
	Format    sheet.Format         `bun:"format,notnull"     json:"format"`
	Status    Status               `bun:"status,notnull"     json:"status"`
	// TotalRows 是预计的行数，由 Writer.SetTotal 设置；0 表示不知道。
	TotalRows     int `bun:"total_rows,notnull"     json:"total_rows"`
	ProcessedRows int `bun:"processed_rows,notnull" json:"processed_rows"` // 已经写出的行数，不含表头
	// FileID、Filename、URL 和 ExpiresAt 在 completed 时写入：上传的文件、它的公开地址和被回收的时间。
	FileID    uuid.UUID  `bun:"file_id,type:uuid,nullzero" json:"file_id,omitzero"`
	Filename  string     `bun:"filename,notnull"           json:"filename,omitempty"`
	URL       string     `bun:"url,notnull"                json:"url,omitempty"`
	ExpiresAt *time.Time `bun:"expires_at"                 json:"expires_at,omitempty"`
	// Error 是 failed 时的错误码，ErrorData 是翻译它的模板数据，见 ErrorMessage。
	Error       string         `bun:"error,notnull"      json:"error,omitempty"`
	ErrorData   map[string]any `bun:"error_data,notnull" json:"-"`
	CreatedAt   time.Time      `bun:"created_at,notnull" json:"created_at"`
	UpdatedAt   time.Time      `bun:"updated_at,notnull" json:"updated_at"`
	CompletedAt *time.Time     `bun:"completed_at"       json:"completed_at,omitempty"` // 变为 completed 或 failed 的时间
}

// Owner 返回发起导出的主体。它只有 ID、Type 和 Tenant，没有权限。
func (exp *Export) Owner() identity.Subject {
	return identity.Subject{ID: exp.OwnerID, Type: exp.OwnerType, Tenant: exp.Tenant}
}

// ErrorMessage 按 ctx 中的语言翻译 Error；没有错误时返回空字符串。
func (exp *Export) ErrorMessage(ctx context.Context) string {
	if exp.Error == "" {
		return ""
	}
	return i18n.T(ctx, exp.Error, exp.ErrorData)
}

// Finished 报告导出是否已经结束（completed 或 failed）。
func (exp *Export) Finished() bool {
	return exp.Status == StatusCompleted || exp.Status == StatusFailed
}

// Service 登记、处理和查询导出。
type Service struct {
	db    *bun.DB
	files *filesystem.Service
}

// New 返回导出服务，files 保存导出的文件。
func New(db *bun.DB, files *filesystem.Service) *Service {
	return &Service{db: db, files: files}
}

// Start 登记 owner 对 kind 的一次导出，返回 pending 状态的导出。format 不是 CSV 或 XLSX 时返回 sheet.ErrFormat。
// tx 通常是发布导出任务的事务，在同一个事务中用 queue.Database(tx) 发布任务，任务中调用 Process。
func (s *Service) Start(ctx context.Context, tx bun.IDB, owner identity.Subject, kind string, format sheet.Format) (*Export, error) {
	if format != sheet.CSV && format != sheet.XLSX {
		return nil, sheet.ErrFormat.New()
	}
	now := time.Now().UTC()
	exp := &Export{
		ID: uuid.Must(uuid.NewV7()), Tenant: owner.Tenant, OwnerType: owner.Type, OwnerID: owner.ID,
		Kind: kind, Format: format, Status: StatusPending, ErrorData: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	_, err := tx.NewInsert().Model(exp).Exec(ctx)
	return exp, errors.Wrap(err, "exports: start")
}

// Get 返回 owner 发起的导出；不存在或者不是 owner 发起的时返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, owner identity.Subject, id uuid.UUID) (*Export, error) {
	exp := new(Export)
	err := s.db.NewSelect().Model(exp).Where("id = ?", id).
		Where("tenant = ?", owner.Tenant).Where("owner_type = ?", owner.Type).Where("owner_id = ?", owner.ID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New()
	}
	return exp, errors.Wrap(err, "exports: get")
}

// saveInterval 是两次保存进度之间的最短时间。
var saveInterval = time.Second

// Writer 写出导出的表格，并记录进度：每秒最多保存一次已经写出的行数。第一行是表头，不计入行数。
type Writer struct {
	w      *sheet.Writer
	run    *run
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
	w.run.exp.ProcessedRows++
	if time.Since(w.last) < saveInterval {
		return nil
	}
	w.last = time.Now()
	return w.run.save(w.run.ctx, "processed_rows")
}

// SetTotal 设置预计的行数并立即保存，前端据此显示进度百分比。不调用时 TotalRows 为 0。
func (w *Writer) SetTotal(n int) error {
	w.run.exp.TotalRows = n
	return w.run.save(w.run.ctx, "total_rows")
}

// run 是 Process 的一次执行。
type run struct {
	s     *Service
	ctx   context.Context
	exp   *Export
	saved func(ctx context.Context, tx database.Tx, exp *Export) error
}

// Process 处理导出 id，在队列任务中调用。
//
// 导出变为 running 后，write 把行写入 w，第一行是表头；写入的内容先保存在本地临时文件中，
// write 返回后以 temporary 状态上传到文件存储，导出变为 completed，URL 和 ExpiresAt 是文件的公开地址和回收时间。
// 文件名是 kind 加上日期和扩展名，例如 articles-20261111.xlsx。
//
// 每次保存进度（变为 running、SetTotal、写出过程中每秒最多一次、结束）都在同一个事务中调用 saved（可以为 nil），
// exp 是保存后的导出。用 tx.AfterCommit 广播进度，供 SSE 推送给正在查看的用户；exp.Finished 为 true 时
// 导出已经结束，例如在这个事务中发送通知。
//
// write 返回带错误码的业务错误（例如 sheet.ErrUnknownColumn）时导出变为 failed，Error 是错误码；
// 其他错误照常返回给队列，重试时从头重新导出。队列的最后一次执行仍然出错时导出变为 failed，Error 是 exports.failed。
// 导出不存在（例如已被 Cleanup 删除）或者已经结束时，Process 什么也不做，返回 nil。
func (s *Service) Process(ctx context.Context, id uuid.UUID,
	write func(ctx context.Context, exp *Export, w *Writer) error,
	saved func(ctx context.Context, tx database.Tx, exp *Export) error,
) error {
	exp := new(Export)
	err := s.db.NewSelect().Model(exp).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "exports: load")
	}
	if exp.Finished() {
		return nil
	}
	r := &run{s: s, ctx: ctx, exp: exp, saved: saved}
	err = r.export(ctx, write)
	switch {
	case err == nil:
		return nil
	case !retryable(err):
		return r.fail(context.WithoutCancel(ctx), err)
	}
	if _, final := queue.AttemptFromContext(ctx); final {
		return errors.CombineErrors(err, r.fail(context.WithoutCancel(ctx), err))
	}
	return err
}

func (r *run) export(ctx context.Context, write func(ctx context.Context, exp *Export, w *Writer) error) error {
	// 重试时从头开始。
	r.exp.Status, r.exp.TotalRows, r.exp.ProcessedRows = StatusRunning, 0, 0
	if err := r.save(ctx, "status", "total_rows", "processed_rows"); err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "export-*."+string(r.exp.Format))
	if err != nil {
		return errors.Wrap(err, "exports: create temporary file")
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	w := &Writer{w: sheet.NewWriter(tmp, r.exp.Format), run: r, last: time.Now()}
	if err := errors.CombineErrors(write(ctx, r.exp, w), w.w.Close()); err != nil {
		return err
	}
	size, err := tmp.Seek(0, io.SeekCurrent)
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err != nil {
		return errors.Wrap(err, "exports: rewind temporary file")
	}
	file, err := r.s.files.Upload(ctx, r.exp.Owner(), filesystem.FileInput{
		Filename:    r.exp.Kind + "-" + time.Now().Format("20060102") + "." + string(r.exp.Format),
		ContentType: r.exp.Format.ContentType(),
		Size:        size,
	}, tmp)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	r.exp.Status, r.exp.CompletedAt = StatusCompleted, &now
	r.exp.FileID, r.exp.Filename, r.exp.URL, r.exp.ExpiresAt = file.ID, file.Filename, file.URL, file.ExpiresAt
	return r.save(ctx, "status", "processed_rows", "file_id", "filename", "url", "expires_at", "completed_at")
}

// fail 把导出标记为 failed。
func (r *run) fail(ctx context.Context, cause error) error {
	now := time.Now().UTC()
	r.exp.Status, r.exp.CompletedAt = StatusFailed, &now
	r.exp.Error, r.exp.ErrorData = errorx.CodeOf(cause), errorx.PublicOf(cause)
	if r.exp.Error == "" || retryable(cause) {
		r.exp.Error, r.exp.ErrorData = "exports.failed", nil
	}
	if r.exp.ErrorData == nil {
		r.exp.ErrorData = map[string]any{}
	}
	return r.save(ctx, "status", "processed_rows", "error", "error_data", "completed_at")
}

// save 在事务中更新 columns 并调用 saved。导出已经结束时（例如另一次执行先完成了）不做修改。
func (r *run) save(ctx context.Context, columns ...string) error {
	r.exp.UpdatedAt = time.Now().UTC()
	return database.RunInTx(ctx, r.s.db, nil, func(ctx context.Context, tx database.Tx) error {
		res, err := tx.NewUpdate().Model(r.exp).Column(append(columns, "updated_at")...).WherePK().
			Where("status IN (?)", bun.In([]Status{StatusPending, StatusRunning})).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "exports: save progress")
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.CombineErrors(errors.New("exports: export already finished"), err)
		}
		if r.saved == nil {
			return nil
		}
		return r.saved(ctx, tx, r.exp)
	})
}

// retryable 报告 err 是否是重试可能解决的错误：不带错误码的错误、服务不可用和资源暂时不足。
func retryable(err error) bool {
	switch errorx.KindOf(err) {
	case errorx.Internal, errorx.Unavailable, errorx.ResourceExhausted:
		return true
	}
	return false
}

// Cleanup 返回删除创建时间早于 keep 之前的导出记录的函数，每次最多删除 limit 条，返回删除的条数。
// 导出的文件是 temporary 状态，由 filesystem 的 Cleanup 回收。可以直接交给 schedule.Cleanup。
func (s *Service) Cleanup(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("exports: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("exports: cleanup limit must be positive")
		}
		old := s.db.NewSelect().Model((*Export)(nil)).Column("id").
			Where("created_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("created_at").Limit(int64(limit))
		res, err := s.db.NewDelete().Model((*Export)(nil)).Where("id IN (SELECT id FROM (?) AS old)", old).Exec(ctx)
		if err != nil {
			return 0, errors.Wrap(err, "exports: cleanup")
		}
		n, err := res.RowsAffected()
		return int(n), errors.Wrap(err, "exports: cleanup")
	}
}
