package queue

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
)

// ErrFailedJobNotFound 表示失败任务表中没有这个 ID。
var ErrFailedJobNotFound = errors.New("queue: failed job not found")

// FailedJobs 管理 queue_failed_jobs 中的失败任务，与队列驱动无关：
// 任何驱动的任务失败后都写到这里，记录来自哪个驱动。失败任务一直保留，直到被重新入队或清理。
type FailedJobs struct {
	db      bun.IDB
	drivers map[string]Driver
}

// NewFailedJobs 创建失败任务管理器。db 必须已执行 Migrations 中的迁移。
// drivers 是 Retry 可以放回的其他驱动；数据库驱动总是可用，放回 db 本身的 queue_jobs。
func NewFailedJobs(db bun.IDB, drivers ...Driver) *FailedJobs {
	if db == nil {
		panic("queue: nil database")
	}
	f := &FailedJobs{db: db, drivers: map[string]Driver{}}
	for _, d := range drivers {
		f.drivers[d.Name()] = d
	}
	return f
}

// Retry 把失败任务放回原来的驱动和 topic，立即可被处理，执行次数从 0 开始计算。
// 任务不存在时返回 ErrFailedJobNotFound；原来的驱动不在 NewFailedJobs 的 drivers 中时返回错误。
// 放回数据库驱动与删除失败记录在同一个事务中；放回其他驱动在删除提交之前完成，提交失败时任务可能被执行两次。
func (f *FailedJobs) Retry(ctx context.Context, id uuid.UUID) error {
	err := f.db.RunInTx(ctx, txOptions(f.db), func(ctx context.Context, tx bun.Tx) error {
		row := new(failedJob)
		err := tx.NewSelect().Model(row).Where("id = ?", id).Scan(ctx)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFailedJobNotFound
		}
		if err != nil {
			return err
		}
		// 删除成功才重新入队：同一个任务被并发 Retry 时只有一方生效。
		res, err := tx.NewDelete().Model((*failedJob)(nil)).Where("id = ?", id).Exec(ctx)
		if err != nil {
			return err
		}
		if n, err := res.RowsAffected(); err != nil || n == 0 {
			return errors.Join(err, ErrFailedJobNotFound)
		}
		d, ok := f.drivers[row.Driver]
		switch {
		case row.Driver == driverDatabase:
			d = Database(tx)
		case !ok:
			return errors.Newf("queue: driver %q is not registered", row.Driver)
		}
		return d.Enqueue(ctx, Job{ID: row.ID, Topic: row.Topic, Payload: row.Payload, Metadata: row.Metadata})
	})
	if errors.Is(err, ErrFailedJobNotFound) {
		return err
	}
	return errors.Wrapf(err, "queue: retry failed job %s", id)
}

// Cleanup 删除最多 limit 条在 before 之前失败的任务，返回删除的条数。由应用定时调用。
func (f *FailedJobs) Cleanup(ctx context.Context, before time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("queue: cleanup limit must be positive")
	}
	old := f.db.NewSelect().Model((*failedJob)(nil)).Column("id").
		Where("failed_at < ?", before.UTC()).OrderExpr("failed_at").Limit(int64(limit))
	res, err := f.db.NewDelete().Model((*failedJob)(nil)).
		Where("id IN (SELECT id FROM (?) AS old)", old).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "queue: cleanup failed jobs")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "queue: cleanup failed jobs")
}

// record 写入一条失败记录。
func (f *FailedJobs) record(ctx context.Context, row failedJob) error {
	return f.db.RunInTx(ctx, txOptions(f.db), func(ctx context.Context, tx bun.Tx) error {
		return recordFailed(ctx, tx, row)
	})
}

// recordFailed 在 tx 中写入失败记录。同一个任务之前失败过、重新入队后又失败时，替换旧的记录。
func recordFailed(ctx context.Context, tx bun.Tx, row failedJob) error {
	if _, err := tx.NewDelete().Model((*failedJob)(nil)).Where("id = ?", row.ID).Exec(ctx); err != nil {
		return err
	}
	if row.Metadata == nil {
		row.Metadata = map[string]string{}
	}
	if row.Payload == nil {
		row.Payload = []byte{}
	}
	_, err := tx.NewInsert().Model(&row).Exec(ctx)
	return err
}
