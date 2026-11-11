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
//
// 带 Unique 键的任务靠唯一索引 (topic, unique_key) 合并，冲突时什么也不做，不会让外层事务中止；
// 并发发布同一个键时，后者等前者的事务结束：前者提交则跳过，回滚则插入。
// MySQL 使用不修改任何列的 upsert，它不区分冲突的是哪个唯一键，因此带键的任务 ID 重复时也会被跳过；
// Publish 每次生成新的 ID，只有 Publisher 重复使用消息 UUID 时才会遇到。
func (d *dbDriver) Enqueue(ctx context.Context, jobs ...Job) error {
	now := time.Now().UTC()
	var plain, keyed []job
	seen := map[[2]string]bool{}
	for _, j := range jobs {
		row := job{ID: j.ID, Topic: j.Topic, Payload: j.Payload, Metadata: j.Metadata, AvailableAt: now, UniqueKey: j.Unique, CreatedAt: now}
		if j.AvailableAt.After(now) {
			row.AvailableAt = j.AvailableAt.UTC()
		}
		if row.Payload == nil {
			row.Payload = []byte{}
		}
		if row.Metadata == nil {
			row.Metadata = map[string]string{}
		}
		if j.Unique == "" {
			plain = append(plain, row)
			continue
		}
		if k := [2]string{j.Topic, j.Unique}; !seen[k] {
			seen[k] = true
			keyed = append(keyed, row)
		}
	}
	if len(plain) > 0 {
		if _, err := d.db.NewInsert().Model(&plain).Exec(ctx); err != nil {
			return err
		}
	}
	if len(keyed) > 0 {
		q := d.db.NewInsert().Model(&keyed)
		if d.db.Dialect().Name() == dialect.MySQL {
			q = q.On("DUPLICATE KEY UPDATE id = id")
		} else {
			q = q.On("CONFLICT (topic, unique_key) DO NOTHING")
		}
		if _, err := q.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
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

// claim 领取 topic 中最多 n 个到期的任务：attempts 加一，available_at 推到租约到期时间，释放 Unique 键。
// 返回的行保留领取前的 available_at，即任务本次可以被领取的时间。
//
// 先锁住到期的行再更新，两步在一个事务中：PostgreSQL 和 MySQL 用 SKIP LOCKED 跳过其他实例正在领取的行，
// MySQL 在读已提交隔离级别下只锁返回的行、不加间隙锁，不会挡住同时发布的任务；
// SQLite 的写事务在 BEGIN 时就取得写锁，同一时刻只有一个领取。
func (d *dbDriver) claim(req ClaimRequest) ([]job, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()
	now := time.Now().UTC()
	var jobs []job
	err := d.db.RunInTx(ctx, txOptions(d.db), func(ctx context.Context, tx bun.Tx) error {
		q := tx.NewSelect().Model(&jobs).Where("j.topic = ?", req.Topic).Where("j.available_at <= ?", now).
			OrderExpr("j.available_at, j.id").Limit(int64(req.Max))
		if d.db.Dialect().Name() != dialect.SQLite {
			q = q.For("UPDATE SKIP LOCKED")
		}
		if err := q.Scan(ctx); err != nil || len(jobs) == 0 {
			return err
		}
		ids := make([]uuid.UUID, len(jobs))
		for i := range jobs {
			ids[i] = jobs[i].ID
		}
		_, err := tx.NewUpdate().Model((*job)(nil)).
			Set("available_at = ?", now.Add(req.Lease)).
			Set("reserved_at = ?", now).
			Set("unique_key = NULL").
			Set("attempts = j.attempts + 1").
			Where("j.id IN (?)", bun.List(ids)).
			Exec(ctx)
		for i := range jobs {
			jobs[i].Attempts++
			jobs[i].ReservedAt = now
		}
		return err
	})
	return jobs, errors.Wrapf(err, "queue: claim %q", req.Topic)
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
