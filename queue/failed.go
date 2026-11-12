package queue

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
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

// RetryAll 把最多 limit 条 topic 中、before 之前失败的任务放回原来的驱动，返回放回的条数。
// topic 为空时包括全部 topic。只处理 before 之前失败的任务，重试后马上又失败的任务不会在同一轮中被反复放回：
// 后台页面上的「全部重试」传入点击时的时间，反复调用直到返回的条数小于 limit。
//
// 原来的驱动不在 NewFailedJobs 的 drivers 中的任务被跳过，留在失败任务表中。
// 每一批在一个事务中删除失败记录并入队，与 Retry 的保证相同。
func (f *FailedJobs) RetryAll(ctx context.Context, topic string, before time.Time, limit int) (int, error) {
	if err := checkBulk(before, limit); err != nil {
		return 0, err
	}
	drivers := []string{driverDatabase}
	for name := range f.drivers {
		drivers = append(drivers, name)
	}
	var n int
	err := f.db.RunInTx(ctx, txOptions(f.db), func(ctx context.Context, tx bun.Tx) error {
		var rows []failedJob
		sel := f.matching(tx.NewSelect().Model(&rows), topic, before).
			Where("f.driver IN (?)", bun.List(drivers)).Limit(int64(limit))
		if f.db.Dialect().Name() != dialect.SQLite {
			sel = sel.For("UPDATE SKIP LOCKED") // 两个管理员同时点「全部重试」时互不重复
		}
		if err := sel.Scan(ctx); err != nil || len(rows) == 0 {
			return err
		}
		ids := make([]uuid.UUID, len(rows))
		byDriver := map[string][]Job{}
		for i, r := range rows {
			ids[i] = r.ID
			byDriver[r.Driver] = append(byDriver[r.Driver], Job{ID: r.ID, Topic: r.Topic, Payload: r.Payload, Metadata: r.Metadata})
		}
		if _, err := tx.NewDelete().Model((*failedJob)(nil)).Where("id IN (?)", bun.List(ids)).Exec(ctx); err != nil {
			return err
		}
		for name, jobs := range byDriver {
			d := f.drivers[name]
			if name == driverDatabase {
				d = Database(tx)
			}
			if err := d.Enqueue(ctx, jobs...); err != nil {
				return err
			}
		}
		n = len(rows)
		return nil
	})
	if err != nil {
		return 0, errors.Wrap(err, "queue: retry failed jobs")
	}
	return n, nil
}

// DeleteAll 删除最多 limit 条 topic 中、before 之前失败的任务，返回删除的条数。topic 为空时包括全部 topic。
// 用于后台页面上的「全部删除」；按保留时间定期清理用 Cleanup。
func (f *FailedJobs) DeleteAll(ctx context.Context, topic string, before time.Time, limit int) (int, error) {
	if err := checkBulk(before, limit); err != nil {
		return 0, err
	}
	n, err := f.delete(ctx, topic, before, limit)
	return n, errors.Wrap(err, "queue: delete failed jobs")
}

// checkBulk 检查批量操作的参数。
func checkBulk(before time.Time, limit int) error {
	switch {
	case limit <= 0:
		return errors.New("queue: limit must be positive")
	case before.IsZero():
		return errors.New("queue: before is required")
	}
	return nil
}

// matching 限定 topic（为空时不限）和失败时间，按失败时间从旧到新。
func (f *FailedJobs) matching(q *bun.SelectQuery, topic string, before time.Time) *bun.SelectQuery {
	q = q.Where("f.failed_at < ?", before.UTC()).OrderExpr("f.failed_at, f.id")
	if topic != "" {
		q = q.Where("f.topic = ?", topic)
	}
	return q
}

// delete 删除最多 limit 条符合条件的失败任务。
func (f *FailedJobs) delete(ctx context.Context, topic string, before time.Time, limit int) (int, error) {
	ids := f.matching(f.db.NewSelect().Model((*failedJob)(nil)).Column("f.id"), topic, before).Limit(int64(limit))
	res, err := f.db.NewDelete().Model((*failedJob)(nil)).
		Where("id IN (SELECT id FROM (?) AS old)", ids).Exec(ctx)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// FailedJob 是一条失败任务记录。
type FailedJob struct {
	ID       uuid.UUID
	Driver   string
	Topic    string
	Payload  []byte
	Metadata map[string]string
	// Attempts 是失败前执行的次数。
	Attempts int
	// Error 是最后一次的错误，包括堆栈。
	Error    string
	FailedAt time.Time
}

func (r *failedJob) export() FailedJob {
	return FailedJob{ID: r.ID, Driver: r.Driver, Topic: r.Topic, Payload: r.Payload, Metadata: r.Metadata,
		Attempts: r.Attempts, Error: r.Error, FailedAt: r.FailedAt}
}

// FailedQuery 是 FailedJobs.List 的条件。
type FailedQuery struct {
	// Topic 为空时列出全部 topic。
	Topic string
	// Limit 是最多返回的条数，0 表示 100。
	Limit  int
	Offset int
}

// List 按失败时间从新到旧列出失败任务。
func (f *FailedJobs) List(ctx context.Context, q FailedQuery) ([]FailedJob, error) {
	if q.Limit <= 0 {
		q.Limit = 100
	}
	var rows []failedJob
	sel := f.db.NewSelect().Model(&rows).OrderExpr("f.failed_at DESC, f.id").Limit(int64(q.Limit)).Offset(int64(max(q.Offset, 0)))
	if q.Topic != "" {
		sel = sel.Where("f.topic = ?", q.Topic)
	}
	if err := sel.Scan(ctx); err != nil {
		return nil, errors.Wrap(err, "queue: list failed jobs")
	}
	jobs := make([]FailedJob, len(rows))
	for i := range rows {
		jobs[i] = rows[i].export()
	}
	return jobs, nil
}

// Get 返回一条失败任务，不存在时返回 ErrFailedJobNotFound。
func (f *FailedJobs) Get(ctx context.Context, id uuid.UUID) (FailedJob, error) {
	row := new(failedJob)
	err := f.db.NewSelect().Model(row).Where("f.id = ?", id).Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return FailedJob{}, ErrFailedJobNotFound
	}
	if err != nil {
		return FailedJob{}, errors.Wrapf(err, "queue: get failed job %s", id)
	}
	return row.export(), nil
}

// Delete 删除一条失败任务，不存在时返回 ErrFailedJobNotFound。
func (f *FailedJobs) Delete(ctx context.Context, id uuid.UUID) error {
	res, err := f.db.NewDelete().Model((*failedJob)(nil)).Where("id = ?", id).Exec(ctx)
	if err != nil {
		return errors.Wrapf(err, "queue: delete failed job %s", id)
	}
	if n, err := res.RowsAffected(); err != nil || n == 0 {
		return errors.Join(err, ErrFailedJobNotFound)
	}
	return nil
}

// count 返回驱动 driver 每个 topic 的失败任务数。
func (f *FailedJobs) count(ctx context.Context, driver string) (map[string]int, error) {
	var rows []struct {
		Topic string `bun:"topic"`
		N     int    `bun:"n"`
	}
	err := f.db.NewSelect().Model((*failedJob)(nil)).Column("f.topic").ColumnExpr("COUNT(*) AS n").
		Where("f.driver = ?", driver).Group("f.topic").Scan(ctx, &rows)
	if err != nil {
		return nil, errors.Wrap(err, "queue: count failed jobs")
	}
	counts := make(map[string]int, len(rows))
	for _, r := range rows {
		counts[r.Topic] = r.N
	}
	return counts, nil
}

// Cleanup 返回删除 keep 以前失败的任务的清理函数，每次调用最多删除 limit 条，返回删除的条数，交给 schedule.Cleanup：
//
//	sched.Add("queue:prune-failed", "@daily", schedule.Cleanup(failed.Cleanup(30*24*time.Hour), 1000))
func (f *FailedJobs) Cleanup(keep time.Duration) func(ctx context.Context, limit int) (int, error) {
	if keep <= 0 {
		panic("queue: keep must be positive")
	}
	return func(ctx context.Context, limit int) (int, error) {
		if limit <= 0 {
			return 0, errors.New("queue: cleanup limit must be positive")
		}
		n, err := f.delete(ctx, "", time.Now().Add(-keep), limit)
		return n, errors.Wrap(err, "queue: cleanup failed jobs")
	}
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
