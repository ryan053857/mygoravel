package operation

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/queue"
)

// errChanged 表示操作已被另一次执行保存过，例如任务的租约过期后被重新领取。本次执行停下，由队列稍后重试。
var errChanged = errors.New("operation: changed by another run")

// Run 是 Service.Run 的一次执行。SetTotal、Add 和 Restart 只修改内存中的进度，Save 把它保存下来。
type Run struct {
	s     *Service
	op    Operation // 最近一次保存的操作
	saved func(ctx context.Context, tx database.Tx, op *Operation) error

	total     int
	hasTotal  bool
	processed int // 上次保存之后增加的单位数
	failed    int
	restart   bool
}

// Operation 返回最近一次保存的操作的副本，不包括还没有保存的进度。
func (r *Run) Operation() *Operation {
	op := r.op
	return &op
}

// SetTotal 设置预计的单位数，前端据此显示进度百分比。
func (r *Run) SetTotal(n int) {
	r.total, r.hasTotal = n, true
}

// Add 记下又处理了 processed 个单位，其中 failed 个失败。
func (r *Run) Add(processed, failed int) {
	r.processed += processed
	r.failed += failed
}

// Restart 把已处理、失败和预计的单位数清零，下次保存时生效。每次重试都从头开始的操作（例如导出）在开头调用它。
func (r *Run) Restart() {
	r.restart = true
	r.total, r.hasTotal = 0, true
	r.processed, r.failed = 0, 0
}

// Save 在一个事务中保存进度，同一个事务中依次调用 write（可以为 nil）和 Service.Run 的 saved。
// write 写入与这次进度一起提交的数据，例如导入的失败行和处理到的行号：
// 进度和这些数据要么一起保存，要么都没有保存，重试时从上次保存的地方继续。
//
// 操作已被另一次执行保存过（例如任务的租约过期后被重新领取）时返回错误，本次执行应当停下。
func (r *Run) Save(ctx context.Context, write func(ctx context.Context, tx database.Tx) error) error {
	return r.save(ctx, r.next(StatusRunning), write)
}

// next 返回带着还没有保存的进度、状态为 status 的操作。
func (r *Run) next(status Status) Operation {
	op := r.op
	if r.restart {
		op.Processed, op.Failed = 0, 0
	}
	if r.hasTotal {
		op.Total = r.total
	}
	op.Processed += r.processed
	op.Failed += r.failed
	op.Status = status
	return op
}

// save 把操作保存为 next，条件是保存期间没有别的执行保存过它。
func (r *Run) save(ctx context.Context, next Operation, write func(ctx context.Context, tx database.Tx) error) error {
	now := time.Now().UTC()
	next.Version++
	next.UpdatedAt = now
	if next.Finished() {
		next.CompletedAt = &now
	}
	err := database.RunInTx(ctx, r.s.db, nil, func(ctx context.Context, tx database.Tx) error {
		res, err := tx.NewUpdate().Model(&next).
			Column("status", "total", "processed", "failed", "version", "error", "error_data", "updated_at", "completed_at").
			WherePK().Where("version = ?", r.op.Version).Exec(ctx)
		if err != nil {
			return errors.Wrap(err, "operation: save")
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return errors.CombineErrors(errChanged, err)
		}
		if write != nil {
			if err := write(ctx, tx); err != nil {
				return err
			}
		}
		if r.saved == nil {
			return nil
		}
		op := next
		return r.saved(ctx, tx, &op)
	})
	if err != nil {
		return err
	}
	r.op = next
	r.processed, r.failed, r.hasTotal, r.restart = 0, 0, false, false
	return nil
}

// Run 执行操作 id，在队列任务中调用。
//
// 操作是 pending 时先变为 running，然后调用 fn。fn 用 r 报告进度，需要时用 r.Save 保存；
// fn 返回 nil 时操作变为 completed，还没有保存的进度一起保存。
//
// fn 返回带错误码的业务错误（例如文件缺少必需的列）时，操作变为 failed，Error 是错误码，Run 返回 nil。
// 其他错误照常返回给队列重试，重试时 Run 再次调用 fn，从上次保存的进度继续，没有保存的进度丢弃；
// 队列的最后一次执行仍然出错，或者错误被 queue.Permanent 标记时，操作变为 failed，Error 是 operation.failed，
// 错误照常返回给队列。
//
// 每次保存（变为 running、r.Save、结束）都在同一个事务中调用 saved（可以为 nil），op 是保存后的操作。
// 用 tx.AfterCommit 广播进度，供 SSE 推送给正在查看的用户；op.Finished 为 true 时操作已经结束，
// 例如在这个事务中发送通知。结束的事务提交后 saved 不会再被调用，所以通知不会重复，也不会漏发。
//
// 操作不存在（例如已被 Cleanup 删除）或者已经结束时，Run 什么也不做，返回 nil。
func (s *Service) Run(ctx context.Context, id uuid.UUID,
	fn func(ctx context.Context, r *Run) error,
	saved func(ctx context.Context, tx database.Tx, op *Operation) error,
) error {
	op := new(Operation)
	err := s.db.NewSelect().Model(op).Where("id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return errors.Wrap(err, "operation: load")
	}
	if op.Finished() {
		return nil
	}
	r := &Run{s: s, op: *op, saved: saved}
	err = r.run(ctx, fn)
	switch {
	case err == nil:
		return nil
	case !retryable(err):
		return r.fail(context.WithoutCancel(ctx), err)
	}
	if _, final := queue.AttemptFromContext(ctx); final || queue.IsPermanent(err) {
		return errors.CombineErrors(err, r.fail(context.WithoutCancel(ctx), err))
	}
	return err
}

func (r *Run) run(ctx context.Context, fn func(ctx context.Context, r *Run) error) error {
	if r.op.Status == StatusPending {
		if err := r.save(ctx, r.next(StatusRunning), nil); err != nil {
			return err
		}
	}
	if err := fn(ctx, r); err != nil {
		return err
	}
	return r.save(ctx, r.next(StatusCompleted), nil)
}

// fail 把操作标记为 failed，还没有保存的进度丢弃。
func (r *Run) fail(ctx context.Context, cause error) error {
	op := r.op
	op.Status = StatusFailed
	op.Error, op.ErrorData = errorx.CodeOf(cause), errorx.PublicOf(cause)
	if op.Error == "" || retryable(cause) {
		op.Error, op.ErrorData = "operation.failed", nil
	}
	if op.ErrorData == nil {
		op.ErrorData = map[string]any{}
	}
	return r.save(ctx, op, nil)
}

// retryable 报告 err 是否是重试可能解决的错误：不带错误码的错误、服务不可用和资源暂时不足。
func retryable(err error) bool {
	switch errorx.KindOf(err) {
	case errorx.Internal, errorx.Unavailable, errorx.ResourceExhausted:
		return true
	}
	return false
}
