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

// FailedJobs 管理 queue_failed_jobs 中的失败任务。失败任务一直保留，直到被重新入队或清理。
type FailedJobs struct {
	db bun.IDB
}

// NewFailedJobs 创建失败任务管理器。db 必须已执行 Migrations 中的迁移。
func NewFailedJobs(db bun.IDB) *FailedJobs {
	if db == nil {
		panic("queue: nil database")
	}
	return &FailedJobs{db: db}
}

// Retry 把失败任务放回原 topic，立即可被处理，处理次数从 0 开始计算。
// 任务不存在时返回 ErrFailedJobNotFound。
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
		if row.Driver != driverDatabase {
			return errors.Newf("queue: unsupported driver %q", row.Driver)
		}
		now := time.Now().UTC()
		_, err = tx.NewInsert().Model(&job{
			ID: row.ID, Topic: row.Topic, Payload: row.Payload, Metadata: row.Metadata,
			AvailableAt: now, CreatedAt: now,
		}).Exec(ctx)
		return err
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
