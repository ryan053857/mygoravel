// Package imports 在后台导入表格：记录进度，任务重试时从中断的地方继续，没有导入的行可以下载修改后重新导入。
//
// 用户先通过 filesystem 上传文件，Start 在事务中登记导入，同一个事务发布队列任务；
// 任务调用 Process，逐行解码并交给业务代码处理：
//
//	imp, err := s.Start(ctx, tx, subject, "articles", fileID)
//	err = ImportArticles.Publish(ctx, queue.Database(tx), imp.ID)
//
//	// 队列任务
//	err := imports.Process(ctx, s, id, func(ctx context.Context, imp *imports.Import, rec *sheet.Record[Changes]) error {
//		_, err := create.Handle(ctx, imp.Owner(), rec.Data)
//		return err
//	}, nil)
//
// 进度由 Get 查询，失败的行由 WriteFailures 写成表格，Cleanup 删除旧的导入和上传的文件。
package imports

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/sheet"
)

// ErrNotFound 表示导入不存在，或者不是这个主体发起的。
var ErrNotFound = errorx.Define(errorx.NotFound, "imports.not_found")

// Status 是导入的状态。
type Status string

const (
	StatusPending   Status = "pending"   // 已登记，等待任务处理
	StatusRunning   Status = "running"   // 正在处理
	StatusCompleted Status = "completed" // 每一行都处理过了，其中可能有没有导入的行
	StatusFailed    Status = "failed"    // 整个文件无法导入，或者任务重试用完；原因见 Error
)

// Import 是一次后台导入，也是 imports 表的 Bun 模型。
type Import struct {
	bun.BaseModel `bun:"table:imports,alias:im"`

	ID        uuid.UUID            `bun:"id,pk,type:uuid"      json:"id"` // UUIDv7，按时间递增
	Tenant    string               `bun:"tenant,notnull"       json:"-"`
	OwnerType identity.SubjectType `bun:"owner_type,notnull"   json:"-"`
	OwnerID   string               `bun:"owner_id,notnull"     json:"-"`
	Kind      string               `bun:"kind,notnull"         json:"kind"` // 导入的是什么，由应用定义，例如 articles
	FileID    uuid.UUID            `bun:"file_id,notnull,type:uuid" json:"file_id"`
	Filename  string               `bun:"filename,notnull"     json:"filename"`
	Locale    string               `bun:"locale,notnull"       json:"-"` // 发起导入的请求的语言，处理时用它翻译验证提示
	Status    Status               `bun:"status,notnull"       json:"status"`
	Header    []string             `bun:"header,notnull"       json:"-"` // 文件的表头，开始处理时写入
	// TotalRows 是文件中的行数（不含表头和空行），开始处理时写入。
	TotalRows     int `bun:"total_rows,notnull"     json:"total_rows"`
	ProcessedRows int `bun:"processed_rows,notnull" json:"processed_rows"` // 已经处理的行数，包括没有导入的行
	FailedRows    int `bun:"failed_rows,notnull"    json:"failed_rows"`    // 没有导入的行数
	LastLine      int `bun:"last_line,notnull"      json:"-"`              // 已经处理到的行号
	// Error 是 failed 时的错误码，ErrorData 是翻译它的模板数据，见 ErrorMessage。
	Error       string         `bun:"error,notnull"      json:"error,omitempty"`
	ErrorData   map[string]any `bun:"error_data,notnull" json:"-"`
	CreatedAt   time.Time      `bun:"created_at,notnull" json:"created_at"`
	UpdatedAt   time.Time      `bun:"updated_at,notnull" json:"updated_at"`
	CompletedAt *time.Time     `bun:"completed_at"       json:"completed_at,omitempty"` // 变为 completed 或 failed 的时间
}

// Owner 返回发起导入的主体。它只有 ID、Type 和 Tenant，需要权限时由应用重新加载。
func (imp *Import) Owner() identity.Subject {
	return identity.Subject{ID: imp.OwnerID, Type: imp.OwnerType, Tenant: imp.Tenant}
}

// ErrorMessage 按 ctx 中的语言翻译 Error；没有错误时返回空字符串。
func (imp *Import) ErrorMessage(ctx context.Context) string {
	if imp.Error == "" {
		return ""
	}
	return i18n.T(ctx, imp.Error, imp.ErrorData)
}

// Failure 是没有导入的一行，也是 import_failures 表的 Bun 模型。
type Failure struct {
	bun.BaseModel `bun:"table:import_failures,alias:imf"`

	ImportID uuid.UUID         `bun:"import_id,pk,type:uuid" json:"-"`
	Line     int               `bun:"line,pk"                json:"line"`  // 在文件中的行号
	Cells    []string          `bun:"cells,notnull"          json:"cells"` // 原始单元格，与 Import.Header 对应
	Errors   map[string]string `bun:"errors,notnull"         json:"errors"`
	Message  string            `bun:"message,notnull"        json:"message"` // 用列名写成的错误，见 sheet.Record.Message
}

// Service 登记和查询导入，Process 用它处理导入。
type Service struct {
	db        *bun.DB
	files     *filesystem.Service
	languages *goi18n.Bundle
}

// New 返回导入服务。files 保存上传的文件；languages 是应用的语言目录，
// Process 用它按发起导入的请求的语言翻译验证提示，为 nil 时提示不翻译。
func New(db *bun.DB, files *filesystem.Service, languages *goi18n.Bundle) *Service {
	return &Service{db: db, files: files, languages: languages}
}

// Start 登记 owner 对 kind 的一次导入：确认 owner 上传的文件 fileID，记下 ctx 中的语言，返回 pending 状态的导入。
//
// tx 通常是发布导入任务的事务，在同一个事务中用 queue.Database(tx) 发布任务，任务中调用 Process。
// 文件不是 .csv 或 .xlsx 时返回 sheet.ErrFormat，文件的其他检查见 filesystem.Service.Confirm。
func (s *Service) Start(ctx context.Context, tx bun.IDB, owner identity.Subject, kind string, fileID uuid.UUID) (*Import, error) {
	files := s.files.WithDB(tx)
	file, err := files.Get(ctx, fileID)
	if err != nil {
		return nil, err
	}
	if _, err := sheet.FormatOf(file.Filename); err != nil {
		return nil, err
	}
	if file, err = files.Confirm(ctx, owner, fileID, filesystem.Accept{}); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	imp := &Import{
		ID: uuid.Must(uuid.NewV7()), Tenant: owner.Tenant, OwnerType: owner.Type, OwnerID: owner.ID,
		Kind: kind, FileID: file.ID, Filename: file.Filename, Locale: i18n.Locale(ctx), Status: StatusPending,
		Header: []string{}, ErrorData: map[string]any{}, CreatedAt: now, UpdatedAt: now,
	}
	_, err = tx.NewInsert().Model(imp).Exec(ctx)
	return imp, errors.Wrap(err, "imports: start")
}

// Get 返回 owner 发起的导入；不存在或者不是 owner 发起的时返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, owner identity.Subject, id uuid.UUID) (*Import, error) {
	imp := new(Import)
	err := s.db.NewSelect().Model(imp).Where("id = ?", id).
		Where("tenant = ?", owner.Tenant).Where("owner_type = ?", owner.Type).Where("owner_id = ?", owner.ID).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound.New()
	}
	return imp, errors.Wrap(err, "imports: get")
}

// Failures 按行号返回 imp 中没有导入的行，最多 limit 行。imp 通常来自 Get，Failures 不再检查发起人。
func (s *Service) Failures(ctx context.Context, imp *Import, limit int) ([]Failure, error) {
	list := []Failure{}
	err := s.db.NewSelect().Model(&list).Where("import_id = ?", imp.ID).OrderExpr("line").Limit(int64(limit)).Scan(ctx)
	return list, errors.Wrap(err, "imports: list failures")
}

// WriteFailures 把 imp 中没有导入的行写成表格：原来的表头加上一列错误，每一行是原始单元格加上 Failure.Message。
// 用户改正之后可以把这个文件重新导入，多出的错误列会被忽略。WriteFailures 不会关闭 w。
func (s *Service) WriteFailures(ctx context.Context, imp *Import, w *sheet.Writer) error {
	row := make([]any, len(imp.Header)+1)
	for i, h := range imp.Header {
		row[i] = h
	}
	row[len(imp.Header)] = i18n.T(ctx, "imports.errors_column", nil)
	if err := w.Write(row...); err != nil {
		return err
	}
	const batch = 500
	after := 0
	for {
		var list []Failure
		err := s.db.NewSelect().Model(&list).Where("import_id = ?", imp.ID).Where("line > ?", after).
			OrderExpr("line").Limit(batch).Scan(ctx)
		if err != nil {
			return errors.Wrap(err, "imports: list failures")
		}
		for _, f := range list {
			for i := range imp.Header {
				row[i] = nil
				if i < len(f.Cells) {
					row[i] = f.Cells[i]
				}
			}
			row[len(imp.Header)] = f.Message
			if err := w.Write(row...); err != nil {
				return err
			}
			after = f.Line
		}
		if len(list) < batch {
			return nil
		}
	}
}

// Cleanup 返回删除创建时间早于 keep 之前的导入的函数，每次最多删除 limit 个导入，返回删除的个数。
// 导入的失败行一起删除，上传的文件标记为删除，由 filesystem 的 Cleanup 回收。可以直接交给 schedule.Cleanup。
func (s *Service) Cleanup(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("imports: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("imports: cleanup limit must be positive")
		}
		var old []Import
		err := s.db.NewSelect().Model(&old).Column("id", "file_id").
			Where("created_at < ?", time.Now().Add(-keep).UTC()).OrderExpr("created_at").Limit(int64(limit)).Scan(ctx)
		if err != nil || len(old) == 0 {
			return 0, errors.Wrap(err, "imports: cleanup")
		}
		ids := make([]uuid.UUID, len(old))
		for i, imp := range old {
			ids[i] = imp.ID
		}
		err = database.RunInTx(ctx, s.db, nil, func(ctx context.Context, tx database.Tx) error {
			if _, err := tx.NewDelete().Model((*Failure)(nil)).Where("import_id IN (?)", bun.In(ids)).Exec(ctx); err != nil {
				return err
			}
			files := s.files.WithDB(tx)
			for _, imp := range old {
				if err := files.Delete(ctx, imp.FileID); err != nil && !errors.Is(err, filesystem.ErrNotFound) {
					return err
				}
			}
			_, err := tx.NewDelete().Model((*Import)(nil)).Where("id IN (?)", bun.In(ids)).Exec(ctx)
			return err
		})
		if err != nil {
			return 0, errors.Wrap(err, "imports: cleanup")
		}
		return len(old), nil
	}
}
