// Package imports 在后台导入表格：记录进度，任务重试时从中断的地方继续，没有导入的行可以下载修改后重新导入。
//
// 一次导入是一个 operation：状态、进度和失败的原因记在 operations 表中，与导出、批量处理出现在同一个任务列表里；
// imports 表只记录上传的文件和处理到的行号。
//
// 用户先通过 filesystem 上传文件，Start 在事务中登记导入，同一个事务发布队列任务；
// 任务调用 Process，逐行解码并交给业务代码处理：
//
//	imp, err := s.Start(ctx, tx, subject, "articles.import", fileID)
//	err = ImportArticles.Publish(ctx, queue.Database(tx), imp.ID)
//
//	// 队列任务
//	err := imports.Process(ctx, s, id, func(ctx context.Context, imp *imports.Import, rec *sheet.Record[Changes]) error {
//		_, err := create.Handle(ctx, imp.Owner(), rec.Data)
//		return err
//	}, nil)
//
// 进度由 Get 查询，失败的行由 WriteFailures 写成表格；operation 的 Cleanup 删除旧的导入。
package imports

import (
	"context"
	"database/sql"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/sheet"
)

// Import 是一次后台导入：嵌入的 Operation 是它的状态和进度，一行是一个单位；
// 其余字段是 imports 表的 Bun 模型。
type Import struct {
	bun.BaseModel       `bun:"table:imports,alias:im"`
	operation.Operation `bun:"-"`

	OperationID uuid.UUID `bun:"operation_id,pk,type:uuid" json:"-"`
	FileID      uuid.UUID `bun:"file_id,notnull,type:uuid" json:"file_id"`
	Filename    string    `bun:"filename,notnull"          json:"filename"`
	Locale      string    `bun:"locale,notnull"            json:"-"` // 发起导入的请求的语言，处理时用它翻译验证提示
	Header      []string  `bun:"header,notnull"            json:"-"` // 文件的表头，开始处理时写入
	LastLine    int       `bun:"last_line,notnull"         json:"-"` // 已经处理到的行号
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
	ops       *operation.Service
	files     *filesystem.Service
	languages *goi18n.Bundle
}

// New 返回导入服务。ops 记录导入的状态和进度；files 保存上传的文件；languages 是应用的语言目录，
// Process 用它按发起导入的请求的语言翻译验证提示，为 nil 时提示不翻译。
func New(db *bun.DB, ops *operation.Service, files *filesystem.Service, languages *goi18n.Bundle) *Service {
	return &Service{db: db, ops: ops, files: files, languages: languages}
}

// Start 登记 owner 发起的一次 kind 导入（例如 articles.import）：确认 owner 上传的文件 fileID，记下 ctx 中的语言，
// 返回 pending 状态的导入。
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
	op, err := s.ops.Start(ctx, tx, owner, kind)
	if err != nil {
		return nil, err
	}
	imp := &Import{
		Operation: *op, OperationID: op.ID,
		FileID: file.ID, Filename: file.Filename, Locale: i18n.Locale(ctx), Header: []string{},
	}
	_, err = tx.NewInsert().Model(imp).Exec(ctx)
	return imp, errors.Wrap(err, "imports: start")
}

// Get 返回 owner 发起的导入；不存在、不是导入或者不是 owner 发起的时返回 operation.ErrNotFound。
func (s *Service) Get(ctx context.Context, owner identity.Subject, id uuid.UUID) (*Import, error) {
	op, err := s.ops.Get(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	imp, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if imp == nil {
		return nil, operation.ErrNotFound.New()
	}
	imp.Operation = *op
	return imp, nil
}

// load 读取导入的文件和行号，不存在时返回 nil。
func (s *Service) load(ctx context.Context, id uuid.UUID) (*Import, error) {
	imp := new(Import)
	err := s.db.NewSelect().Model(imp).Where("operation_id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.Wrap(err, "imports: load")
	}
	return imp, nil
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
