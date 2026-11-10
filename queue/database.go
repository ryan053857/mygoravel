package queue

import (
	"context"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// driverDatabase 是数据库驱动的名称。
const driverDatabase = "database"

// Database 返回把任务保存在 queue_jobs 表中的驱动，支持 PostgreSQL、MySQL 和 SQLite，
// db 必须已执行 Migrations 中的迁移。db 可以是 bun.Tx：在事务中发布的任务随事务一起提交，
// 事务回滚时任务不会被处理。失败任务写入同一个数据库的 queue_failed_jobs，与删除任务在同一个事务中完成。
func Database(db bun.IDB) Driver {
	if db == nil {
		panic("queue: nil database")
	}
	return &dbDriver{db: db}
}

type dbDriver struct {
	db bun.IDB
}

func (d *dbDriver) Name() string { return driverDatabase }

// Enqueue 实现 Driver。同一个 ID 重复入队会返回主键冲突的错误。
func (d *dbDriver) Enqueue(ctx context.Context, jobs ...Job) error {
	if len(jobs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]job, len(jobs))
	for i, j := range jobs {
		rows[i] = job{ID: j.ID, Topic: j.Topic, Payload: j.Payload, Metadata: j.Metadata, AvailableAt: now, CreatedAt: now}
		if j.AvailableAt.After(now) {
			rows[i].AvailableAt = j.AvailableAt.UTC()
		}
		if rows[i].Payload == nil {
			rows[i].Payload = []byte{}
		}
		if rows[i].Metadata == nil {
			rows[i].Metadata = map[string]string{}
		}
	}
	_, err := d.db.NewInsert().Model(&rows).Exec(ctx)
	return err
}

// Claim 实现 Driver：每 req.PollInterval 查询一次，直到领取到任务。
// 单次领取使用独立的超时，ctx 只用于结束等待，不会打断已经开始的领取。
func (d *dbDriver) Claim(ctx context.Context, req ClaimRequest) ([]Claim, error) {
	for {
		jobs, err := d.claim(req)
		if err != nil || len(jobs) > 0 {
			claims := make([]Claim, len(jobs))
			for i := range jobs {
				claims[i] = &dbClaim{db: d.db, row: jobs[i]}
			}
			return claims, err
		}
		t := time.NewTimer(req.PollInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// claim 领取 topic 中最多 n 个到期的任务：attempts 加一，available_at 推到租约到期时间。
func (d *dbDriver) claim(req ClaimRequest) ([]job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	now := time.Now().UTC()
	ready := func(q *bun.SelectQuery) *bun.SelectQuery {
		return q.Where("j.topic = ?", req.Topic).Where("j.available_at <= ?", now).
			OrderExpr("j.available_at, j.id").Limit(int64(req.Max))
	}

	if d.db.Dialect().Name() == dialect.MySQL {
		jobs, err := d.claimMySQL(ctx, ready, now, req.Lease)
		return jobs, errors.Wrapf(err, "queue: claim %q", req.Topic)
	}
	ids := ready(d.db.NewSelect().Model((*job)(nil)).Column("j.id"))
	// PostgreSQL 用 SKIP LOCKED 跳过其他实例正在领取的行；SQLite 同一时刻只有一个写者，整条 UPDATE 本身就是互斥的。
	if d.db.Dialect().Name() == dialect.PG {
		ids = ids.For("UPDATE SKIP LOCKED")
	}
	var jobs []job
	err := d.db.NewUpdate().Model((*job)(nil)).
		Set("available_at = ?", now.Add(req.Lease)).
		Set("reserved_at = ?", now).
		Set("attempts = j.attempts + 1").
		Where("j.id IN (?)", ids).
		Returning("*").
		Scan(ctx, &jobs)
	return jobs, errors.Wrapf(err, "queue: claim %q", req.Topic)
}

// claimMySQL 在一个事务中先锁住到期的行再更新：MySQL 没有 UPDATE … RETURNING，
// 也不允许在 IN 子查询中使用 LIMIT。读已提交隔离级别下 FOR UPDATE 只锁返回的行，不加间隙锁，
// 不会挡住同时发布的任务。
func (d *dbDriver) claimMySQL(ctx context.Context, ready func(*bun.SelectQuery) *bun.SelectQuery, now time.Time, lease time.Duration) ([]job, error) {
	var jobs []job
	err := d.db.RunInTx(ctx, txOptions(d.db), func(ctx context.Context, tx bun.Tx) error {
		if err := ready(tx.NewSelect().Model(&jobs)).For("UPDATE SKIP LOCKED").Scan(ctx); err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		ids := make([]uuid.UUID, len(jobs))
		for i := range jobs {
			ids[i] = jobs[i].ID
		}
		until := now.Add(lease)
		_, err := tx.NewUpdate().Model((*job)(nil)).
			Set("available_at = ?", until).
			Set("reserved_at = ?", now).
			Set("attempts = j.attempts + 1").
			Where("j.id IN (?)", bun.In(ids)).
			Exec(ctx)
		for i := range jobs {
			jobs[i].Attempts++
			jobs[i].AvailableAt = until
			jobs[i].ReservedAt = now
		}
		return err
	})
	return jobs, err
}

// dbClaim 是数据库中的一次领取。attempts 是防护令牌：任务被重新领取后 attempts 改变，
// 旧的领取对它的确认和修改都不再生效。
type dbClaim struct {
	db  bun.IDB
	row job
}

func (c *dbClaim) Job() Job {
	return Job{
		ID: c.row.ID, Topic: c.row.Topic, Payload: c.row.Payload, Metadata: c.row.Metadata,
		AvailableAt: c.row.AvailableAt, Attempt: c.row.Attempts,
	}
}

func (c *dbClaim) Extend(ctx context.Context, lease time.Duration) error {
	return c.update(ctx, map[string]any{"available_at": time.Now().UTC().Add(lease)})
}

func (c *dbClaim) Ack(ctx context.Context) error {
	res, err := c.db.NewDelete().Model((*job)(nil)).
		Where("id = ?", c.row.ID).Where("attempts = ?", c.row.Attempts).Exec(ctx)
	return held(res, err)
}

func (c *dbClaim) Retry(ctx context.Context, at time.Time) error {
	return c.update(ctx, map[string]any{"available_at": at.UTC(), "reserved_at": nil})
}

func (c *dbClaim) Release(ctx context.Context) error {
	return c.update(ctx, map[string]any{"available_at": time.Now().UTC(), "reserved_at": nil, "attempts": c.row.Attempts - 1})
}

// fail 在一个事务中把仍由本次领取持有的任务移入 queue_failed_jobs。
func (c *dbClaim) fail(ctx context.Context, record failedJob) error {
	return c.db.RunInTx(ctx, txOptions(c.db), func(ctx context.Context, tx bun.Tx) error {
		res, err := tx.NewDelete().Model((*job)(nil)).
			Where("id = ?", c.row.ID).Where("attempts = ?", c.row.Attempts).Exec(ctx)
		if err := held(res, err); err != nil {
			return err
		}
		return recordFailed(ctx, tx, record)
	})
}

// update 在任务仍由本次领取持有时（attempts 未变）更新 set 中的列。
func (c *dbClaim) update(ctx context.Context, set map[string]any) error {
	q := c.db.NewUpdate().Model((*job)(nil)).Where("id = ?", c.row.ID).Where("attempts = ?", c.row.Attempts)
	for col, v := range set {
		q = q.Set("? = ?", bun.Ident(col), v)
	}
	return held(q.Exec(ctx))
}

// held 把“没有更新到行”转换为 ErrLeaseLost。
func held(res interface{ RowsAffected() (int64, error) }, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}
