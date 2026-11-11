package imports

import (
	"context"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/filesystem"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/operation"
	"github.com/ryan053857/mygoravel/sheet"
)

// chunk 是两次保存进度之间处理的行数。
const chunk = 100

// Process 处理导入 id，在队列任务中调用。它用 operation.Service.Run 执行导入，一行是一个单位。
//
// 第一次执行时读出表头并统计行数；然后用 sheet.Decode 逐行解码为 T，验证通过的行交给 row。
// row 用 rec.SetError 标出只属于这一行的问题，例如 slug 已被占用；
// row 返回带错误码的业务错误时，这一行同样记为没有导入，提示是错误码的译文。
// 验证失败和 row 标出错误的行写入失败行，其余的行视为已导入。验证提示和错误的译文使用 Start 时请求的语言。
//
// 每处理 100 行保存一次进度，失败的行和处理到的行号与进度一起提交。row 返回不带错误码的错误（例如数据库不可用）时，
// 先保存进度再返回这个错误，队列重试时从中断的地方继续。进程崩溃时，上次保存之后处理过的行会被再次交给 row，
// 所以 row 要能重复执行，例如按唯一的业务字段判断记录是否已经存在。
//
// 全部行处理完时导入变为 completed。文件无法读取、缺少必需的列等整个文件的问题使导入变为 failed，
// Error 是错误码；队列的最后一次执行仍然出错时，导入同样变为 failed，Error 是 operation.failed。
// 导入结束时上传的文件被删除，由 filesystem 的 Cleanup 回收；下载失败的行只用到表头和失败行。
//
// 每次保存进度都在同一个事务中调用 saved（可以为 nil），imp 是保存后的导入，用法见 operation.Service.Run。
// 导入不存在（例如已被清理）或者已经结束时，Process 什么也不做，返回 nil。
func Process[T any](ctx context.Context, s *Service, id uuid.UUID,
	row func(ctx context.Context, imp *Import, rec *sheet.Record[T]) error,
	saved func(ctx context.Context, tx database.Tx, imp *Import) error,
) error {
	imp, err := s.load(ctx, id)
	if err != nil || imp == nil {
		return err
	}
	if s.languages != nil && imp.Locale != "" {
		ctx = i18n.WithLocale(ctx, s.languages, imp.Locale)
	}
	return s.ops.Run(ctx, id, func(ctx context.Context, r *operation.Run) error {
		imp.Operation = *r.Operation()
		p := &processor[T]{s: s, r: r, imp: imp, row: row}
		return p.run(ctx)
	}, func(ctx context.Context, tx database.Tx, op *operation.Operation) error {
		imp.Operation = *op
		if op.Finished() {
			err := s.files.WithDB(tx).Delete(ctx, imp.FileID)
			if err != nil && !errors.Is(err, filesystem.ErrNotFound) {
				return err
			}
		}
		if saved == nil {
			return nil
		}
		return saved(ctx, tx, imp)
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

type processor[T any] struct {
	s        *Service
	r        *operation.Run
	imp      *Import
	row      func(ctx context.Context, imp *Import, rec *sheet.Record[T]) error
	pending  []Failure // 上次保存之后没有导入的行
	lastLine int       // 处理到的行号，保存后写入 imp.LastLine
	rows     int       // 上次保存之后处理的行数
}

func (p *processor[T]) run(ctx context.Context) error {
	if len(p.imp.Header) == 0 {
		if err := p.begin(ctx); err != nil {
			return err
		}
	}
	return p.open(ctx, func(r *sheet.Reader) error {
		records, err := sheet.Decode[T](ctx, r)
		if err != nil {
			return err
		}
		for rec, err := range records {
			if err != nil {
				return err
			}
			if rec.Line <= p.imp.LastLine {
				continue
			}
			if rec.Valid() {
				if err := p.row(ctx, p.imp, &rec); err != nil {
					if retryable(err) {
						return errors.CombineErrors(err, p.commit(context.WithoutCancel(ctx), false))
					}
					rec.SetError("", i18n.T(ctx, errorx.CodeOf(err), errorx.PublicOf(err)))
				}
			}
			if !rec.Valid() {
				p.pending = append(p.pending, Failure{
					ImportID: p.imp.ID, Line: rec.Line, Cells: rec.Values, Errors: rec.Errors, Message: rec.Message(),
				})
			}
			p.rows++
			p.lastLine = rec.Line
			if p.rows == chunk {
				if err := p.commit(ctx, false); err != nil {
					return err
				}
			}
		}
		return p.commit(ctx, true)
	})
}

// open 打开导入的文件，交给 fn 读取。
func (p *processor[T]) open(ctx context.Context, fn func(r *sheet.Reader) error) error {
	format, err := sheet.FormatOf(p.imp.Filename)
	if err != nil {
		return err
	}
	body, _, err := p.s.files.Open(ctx, p.imp.FileID)
	if err != nil {
		return err
	}
	defer body.Close()
	r, err := sheet.NewReader(body, format)
	if err != nil {
		return err
	}
	defer r.Close()
	return fn(r)
}

// begin 检查表头是否包含必需的列、统计行数，保存表头和总行数。
func (p *processor[T]) begin(ctx context.Context) error {
	var header []string
	total := 0
	err := p.open(ctx, func(r *sheet.Reader) error {
		if _, err := sheet.Decode[T](ctx, r); err != nil {
			return err
		}
		header = r.Header()
		for _, err := range r.Rows() {
			if err != nil {
				return err
			}
			total++
		}
		return nil
	})
	if err != nil {
		return err
	}
	p.r.SetTotal(total)
	err = p.r.Save(ctx, func(ctx context.Context, tx database.Tx) error {
		_, err := tx.NewUpdate().Model(&Import{OperationID: p.imp.ID, Header: header}).Column("header").WherePK().Exec(ctx)
		return errors.Wrap(err, "imports: save header")
	})
	if err != nil {
		return err
	}
	p.imp.Header = header
	return nil
}

// commit 保存进度：写入上次保存之后没有导入的行和处理到的行号，更新计数。final 为 true 时同时结束导入。
func (p *processor[T]) commit(ctx context.Context, final bool) error {
	if p.rows == 0 && !final {
		return nil // 上次保存之后没有处理任何行
	}
	p.r.Add(p.rows, len(p.pending))
	pending, lastLine := p.pending, p.lastLine
	save := p.r.Save
	if final {
		save = p.r.Complete
	}
	err := save(ctx, func(ctx context.Context, tx database.Tx) error {
		if len(pending) > 0 {
			if _, err := tx.NewInsert().Model(&pending).Exec(ctx); err != nil {
				return errors.Wrap(err, "imports: save failures")
			}
		}
		_, err := tx.NewUpdate().Model(&Import{OperationID: p.imp.ID, LastLine: lastLine}).Column("last_line").WherePK().Exec(ctx)
		return errors.Wrap(err, "imports: save progress")
	})
	if err != nil {
		return err
	}
	p.imp.LastLine = lastLine
	p.pending, p.rows = nil, 0
	return nil
}
