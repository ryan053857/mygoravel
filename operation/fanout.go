package operation

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/queue"
)

// Unit 是 Fanout 发布的一个单位，作为队列任务的 payload：所属的操作、单位的 ID 和业务数据。
// 用它声明 topic，例如 queue.Topic[operation.Unit[uuid.UUID]]("articles.summarize")。
type Unit[T any] struct {
	Operation uuid.UUID `json:"operation"`
	ID        uuid.UUID `json:"id"`
	Data      T         `json:"data"`
}

// unit 是还没有成功的单位，也是 operation_units 表的 Bun 模型。单位成功时删除。
type unit struct {
	bun.BaseModel `bun:"table:operation_units,alias:opu"`

	OperationID uuid.UUID `bun:"operation_id,pk,type:uuid"`
	ID          uuid.UUID `bun:"id,pk,type:uuid"`
	Failed      bool      `bun:"failed,notnull"`
}

// insertBatch 是一次插入的单位数。PostgreSQL 一条语句最多 65535 个参数。
const insertBatch = 1000

// Fanout 把 items 加入操作 id：每一项是一个单位，发布为 topic 的一个任务，由 HandleUnit 包装的函数处理，各个任务并行执行。
// 操作的 Total 增加 len(items)，状态变为 running；全部单位都成功或失败之后，操作变为 completed。
//
// tx 是 operations 表所在的数据库，通常是登记操作的事务；d 是发布任务的驱动，用 queue.Database(tx) 时
// 单位和任务一起提交：
//
//	op, err := ops.Start(ctx, tx, subject, "articles.summarize")
//	err = operation.Fanout(ctx, tx, queue.Database(tx), op.ID, SummarizeJob, ids...)
//
// 可以调用多次，例如发布不同 topic 的单位，或者在单位的处理函数中继续加入单位。在别处分批加入时要注意，
// 已经加入的单位可能在下一批加入之前全部结束，操作随之结束。items 为空、操作不存在或者已经结束时返回错误。
// 用 Fanout 执行的操作不要再交给 Service.Run。
func Fanout[T any](ctx context.Context, tx bun.IDB, d queue.Driver, id uuid.UUID, topic queue.Topic[Unit[T]], items ...T) error {
	if len(items) == 0 {
		return errors.New("operation: fanout without items")
	}
	res, err := tx.NewUpdate().Model((*Operation)(nil)).
		Set("total = total + ?", len(items)).Set("status = ?", StatusRunning).
		Set("version = version + 1").Set("updated_at = ?", time.Now().UTC()).
		Where("id = ?", id).Where("status IN (?)", bun.List([]Status{StatusPending, StatusRunning})).Exec(ctx)
	if err != nil {
		return errors.Wrap(err, "operation: fanout")
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return errors.CombineErrors(errors.Newf("operation: fanout to %s, which does not exist or has finished", id), err)
	}
	units := make([]unit, len(items))
	for i := range units {
		units[i] = unit{OperationID: id, ID: uuid.Must(uuid.NewV7())}
	}
	for start := 0; start < len(units); start += insertBatch {
		batch := units[start:min(start+insertBatch, len(units))]
		if _, err := tx.NewInsert().Model(&batch).Exec(ctx); err != nil {
			return errors.Wrap(err, "operation: fanout")
		}
	}
	for i, item := range items {
		if err := topic.Publish(ctx, d, Unit[T]{Operation: id, ID: units[i].ID, Data: item}); err != nil {
			return err
		}
	}
	return nil
}

// HandleUnit 把 fn 包装为 Fanout 发布的单位的处理函数，交给 topic.Handle：
//
//	SummarizeJob.Handle(w, operation.HandleUnit(ops, summarize.Unit, saved))
//
// fn 收到单位所属的操作和业务数据。fn 返回 nil 时单位成功，Processed 加一。
// fn 返回带错误码的业务错误时单位失败，Processed 和 Failed 各加一，任务不再重试。
// 其他错误交给队列重试；队列的最后一次执行仍然出错，或者错误被 queue.Permanent 标记时单位失败，任务转入失败任务表，
// 之后用 queue.FailedJobs 重试并成功时 Failed 减一。
//
// 已经成功的单位再次投递（例如确认任务之前进程崩溃）时不再调用 fn；操作已被删除时同样直接返回。
// fn 仍然要能重复执行：fn 返回之后、记下结果之前进程崩溃，这个单位会再执行一次。
//
// 每个单位的结果都在一个事务中记下，同一个事务调用 saved（可以为 nil），用法见 Service.Run。
// 最后一个单位结束时操作变为 completed，op.Finished() 为 true；操作结束之后 saved 不再被调用，
// 包括重试失败任务修正计数的时候，所以结束通知不会重复。
func HandleUnit[T any](s *Service,
	fn func(ctx context.Context, op *Operation, data T) error,
	saved func(ctx context.Context, tx database.Tx, op *Operation) error,
) func(ctx context.Context, u Unit[T]) error {
	return func(ctx context.Context, u Unit[T]) error {
		op := new(Operation)
		err := s.db.NewSelect().Model(op).Where("id = ?", u.Operation).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return errors.Wrap(err, "operation: load")
		}
		pending, err := s.db.NewSelect().Model((*unit)(nil)).
			Where("operation_id = ?", u.Operation).Where("id = ?", u.ID).Exists(ctx)
		if err != nil {
			return errors.Wrap(err, "operation: load unit")
		}
		if !pending {
			return nil
		}
		err = fn(ctx, op, u.Data)
		switch {
		case err == nil:
			return s.settle(ctx, u.Operation, u.ID, false, saved)
		case !retryable(err):
			return s.settle(context.WithoutCancel(ctx), u.Operation, u.ID, true, saved)
		}
		if _, final := queue.AttemptFromContext(ctx); final || queue.IsPermanent(err) {
			return errors.CombineErrors(err, s.settle(context.WithoutCancel(ctx), u.Operation, u.ID, true, saved))
		}
		return err
	}
}

// settle 在一个事务中记下单位的结果：成功时删除单位，失败时标记为 failed，并更新操作的计数。
// 锁住操作的行，同一个操作的单位依次记下结果。单位已经成功、已经记为失败又失败，或者操作已被删除时什么也不做。
func (s *Service) settle(ctx context.Context, opID, unitID uuid.UUID, failed bool,
	saved func(ctx context.Context, tx database.Tx, op *Operation) error,
) error {
	err := database.RunInTx(ctx, s.db, nil, func(ctx context.Context, tx database.Tx) error {
		op := new(Operation)
		if err := tx.NewSelect().Model(op).Where("id = ?", opID).For("UPDATE").Scan(ctx); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		u := &unit{OperationID: opID, ID: unitID}
		if err := tx.NewSelect().Model(u).WherePK().Scan(ctx); errors.Is(err, sql.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		switch {
		case !failed:
			if _, err := tx.NewDelete().Model(u).WherePK().Exec(ctx); err != nil {
				return err
			}
			if u.Failed {
				op.Failed--
			} else {
				op.Processed++
			}
		case !u.Failed:
			u.Failed = true
			if _, err := tx.NewUpdate().Model(u).Column("failed").WherePK().Exec(ctx); err != nil {
				return err
			}
			op.Processed++
			op.Failed++
		default:
			return nil
		}
		finished := op.Finished()
		now := time.Now().UTC()
		op.Version++
		op.UpdatedAt = now
		if !finished && op.Processed >= op.Total {
			op.Status, op.CompletedAt = StatusCompleted, &now
		}
		_, err := tx.NewUpdate().Model(op).Column("status", "processed", "failed", "version", "updated_at", "completed_at").
			WherePK().Exec(ctx)
		if err != nil || finished || saved == nil {
			return err
		}
		return saved(ctx, tx, op)
	})
	return errors.Wrap(err, "operation: settle unit")
}
