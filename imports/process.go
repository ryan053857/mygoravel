package imports

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/i18n"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/sheet"
)

// chunk 是两次保存进度之间处理的行数。
const chunk = 100

// errMoved 表示进度已被另一次执行改变，例如任务的租约过期后被重新领取。本次执行停下，由队列稍后重试。
var errMoved = errors.New("imports: progress changed by another run")

// Process 处理导入 id，在队列任务中调用。
//
// 第一次执行时读出表头并统计行数，导入变为 running；然后用 sheet.Decode 逐行解码为 T，
// 验证通过的行交给 row。row 用 rec.SetError 标出只属于这一行的问题，例如 slug 已被占用；
// row 返回带错误码的业务错误时，这一行同样记为没有导入，提示是错误码的译文。
// 验证失败和 row 标出错误的行写入失败行，其余的行视为已导入。验证提示和错误的译文使用 Start 时请求的语言。
//
// 每处理 100 行保存一次进度。row 返回不带错误码的错误（例如数据库不可用）时，先保存进度再返回这个错误，
// 队列重试时从中断的地方继续。进程崩溃时，上次保存之后处理过的行会被再次交给 row，所以 row 要能重复执行，
// 例如按唯一的业务字段判断记录是否已经存在。
//
// 全部行处理完时导入变为 completed。文件无法读取、缺少必需的列等整个文件的问题使导入变为 failed，
// Error 是错误码；队列的最后一次执行仍然出错时，导入同样变为 failed，Error 是 imports.failed，
// 错误照常返回给队列。变为 completed 或 failed 时，在同一个事务中调用 done（可以为 nil），
// 例如发送通知；事务提交后 done 不会再被调用。
//
// 导入不存在（例如已被 Cleanup 删除）或者已经结束时，Process 什么也不做，返回 nil。
func Process[T any](ctx context.Context, s *Service, id uuid.UUID,
	row func(ctx context.Context, imp *Import, rec *sheet.Record[T]) error,
	done func(ctx context.Context, tx database.Tx, imp *Import) error,
) error {
	imp := new(Import)
	err := s.db.NewSelect().Model(imp).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "imports: load")
	}
	if imp.Status == StatusCompleted || imp.Status == StatusFailed {
		return nil
	}
	if s.languages != nil && imp.Locale != "" {
		ctx = i18n.WithLocale(ctx, s.languages, imp.Locale)
	}
	p := &processor[T]{s: s, imp: imp, row: row, done: done, lastLine: imp.LastLine}
	err = p.run(ctx)
	switch {
	case err == nil:
		return nil
	case !retryable(err):
		return p.commit(context.WithoutCancel(ctx), StatusFailed, err)
	}
	if _, final := queue.AttemptFromContext(ctx); final {
		return errors.CombineErrors(err, p.commit(context.WithoutCancel(ctx), StatusFailed, err))
	}
	return err
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
	imp      *Import
	row      func(ctx context.Context, imp *Import, rec *sheet.Record[T]) error
	done     func(ctx context.Context, tx database.Tx, imp *Import) error
	pending  []Failure // 上次保存之后没有导入的行
	lastLine int       // 处理到的行号，保存后写入 imp.LastLine
	rows     int       // 上次保存之后处理的行数
}

func (p *processor[T]) run(ctx context.Context) error {
	if p.imp.Status == StatusPending {
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
						return errors.CombineErrors(err, p.commit(context.WithoutCancel(ctx), StatusRunning, nil))
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
				if err := p.commit(ctx, StatusRunning, nil); err != nil {
					return err
				}
			}
		}
		return p.commit(ctx, StatusCompleted, nil)
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

// begin 检查表头是否包含必需的列、统计行数，把导入变为 running。
func (p *processor[T]) begin(ctx context.Context) error {
	imp := *p.imp
	err := p.open(ctx, func(r *sheet.Reader) error {
		if _, err := sheet.Decode[T](ctx, r); err != nil {
			return err
		}
		imp.Header = r.Header()
		for _, err := range r.Rows() {
			if err != nil {
				return err
			}
			imp.TotalRows++
		}
		return nil
	})
	if err != nil {
		return err
	}
	imp.Status, imp.UpdatedAt = StatusRunning, time.Now().UTC()
	res, err := p.s.db.NewUpdate().Model(&imp).Column("status", "header", "total_rows", "updated_at").
		WherePK().Where("status = ?", StatusPending).Exec(ctx)
	if err := moved(res, err); err != nil {
		return err
	}
	*p.imp = imp
	return nil
}

// commit 保存进度：写入上次保存之后没有导入的行，更新计数。status 不是 running 时同时结束导入并调用 done；
// cause 是 failed 的原因。进度已被另一次执行改变时返回 errMoved。
func (p *processor[T]) commit(ctx context.Context, status Status, cause error) error {
	imp := *p.imp
	imp.ProcessedRows += p.rows
	imp.FailedRows += len(p.pending)
	imp.LastLine = p.lastLine
	imp.Status = status
	now := time.Now().UTC()
	imp.UpdatedAt = now
	if status != StatusRunning {
		imp.CompletedAt = &now
	}
	if cause != nil {
		imp.Error, imp.ErrorData = errorx.CodeOf(cause), errorx.PublicOf(cause)
		if imp.Error == "" || retryable(cause) {
			imp.Error, imp.ErrorData = "imports.failed", nil
		}
		if imp.ErrorData == nil {
			imp.ErrorData = map[string]any{}
		}
	}
	err := database.RunInTx(ctx, p.s.db, nil, func(ctx context.Context, tx database.Tx) error {
		if len(p.pending) > 0 {
			if _, err := tx.NewInsert().Model(&p.pending).Exec(ctx); err != nil {
				return errors.Wrap(err, "imports: save failures")
			}
		}
		res, err := tx.NewUpdate().Model(&imp).
			Column("status", "processed_rows", "failed_rows", "last_line", "error", "error_data", "updated_at", "completed_at").
			WherePK().Where("status IN (?)", bun.In([]Status{StatusPending, StatusRunning})).
			Where("last_line = ?", p.imp.LastLine).Exec(ctx)
		if err := moved(res, err); err != nil {
			return err
		}
		if status != StatusRunning && p.done != nil {
			return p.done(ctx, tx, &imp)
		}
		return nil
	})
	if err != nil {
		return err
	}
	*p.imp = imp
	p.pending, p.rows = nil, 0
	return nil
}

// moved 检查条件更新是否改到了一行，没有时返回 errMoved。
func moved(res sql.Result, err error) error {
	if err != nil {
		return errors.Wrap(err, "imports: save progress")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.CombineErrors(errMoved, err)
	}
	return nil
}
