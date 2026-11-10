package ratelimit

import (
	"context"
	"database/sql"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect"
)

// Database 把计数存在数据库中，多个实例共享同一份配额，表结构由 Migrations 提供。
// 每个 key 只存一个理论到达时间（tat），一次 Allow 是一条原子的 upsert，时间取数据库时钟。
//
// 目前只支持 PostgreSQL。每次 Allow 都是一次写入，适合登录、验证码、发邮件这类低频操作；
// 每个请求都要经过的全局限流请使用 Memory。
type Database struct {
	gcra
	db   bun.IDB
	name string
}

// NewDatabase 创建数据库限流器。name 区分共用 rate_limits 表的不同限流器，
// 同名的限流器共享配额，因此 name 应与 cfg 一一对应。
// db 必须已执行 rate_limits 迁移；db 为 nil、name 为空、cfg 不合法或数据库不是 PostgreSQL 时 panic。
func NewDatabase(db bun.IDB, name string, cfg Config) *Database {
	switch {
	case db == nil:
		panic("ratelimit: NewDatabase: nil database")
	case name == "":
		panic("ratelimit: NewDatabase: empty name")
	case db.Dialect().Name() != dialect.PG:
		panic("ratelimit: NewDatabase: only PostgreSQL is supported")
	}
	return &Database{gcra: newGCRA("NewDatabase", cfg), db: db, name: name}
}

// Allow 实现 Limiter。
func (d *Database) Allow(ctx context.Context, key string) error {
	// tat 不超过 now + ahead 时放行并推后 step；否则不更新，RETURNING 不返回行。
	var allowed bool
	err := d.db.NewRaw(`
INSERT INTO rate_limits AS r (name, key, tat)
VALUES (?, ?, statement_timestamp() + ? * interval '1 microsecond')
ON CONFLICT (name, key) DO UPDATE
   SET tat = GREATEST(r.tat, statement_timestamp()) + ? * interval '1 microsecond'
 WHERE r.tat <= statement_timestamp() + ? * interval '1 microsecond'
RETURNING true`,
		d.name, key, d.step.Microseconds(), d.step.Microseconds(), d.ahead.Microseconds()).Scan(ctx, &allowed)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return errors.Wrap(err, "ratelimit: allow")
	}
	var wait int64 // 微秒
	err = d.db.NewRaw(`
SELECT (extract(epoch FROM tat - statement_timestamp()) * 1000000)::bigint - ?
  FROM rate_limits WHERE name = ? AND key = ?`,
		d.ahead.Microseconds(), d.name, key).Scan(ctx, &wait)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return errors.Wrap(err, "ratelimit: allow")
	}
	retry := time.Duration(wait) * time.Microsecond
	if retry <= 0 {
		// 两条语句之间行被 Cleanup 删除或配额恰好回满，按一个 step 估计。
		retry = d.step
	}
	return exceeded(retry)
}

// Cleanup 删除最多 limit 条已回满的行，返回删除的条数。由应用定时调用。
//
// tat 已过去的行与不存在的行等价，删除不会改变任何限流结果。
// 清理作用于整张 rate_limits 表，不限于本限流器的 name，因此任意一个 Database 调用即可。
func (d *Database) Cleanup(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, errors.New("ratelimit: cleanup limit must be positive")
	}
	res, err := d.db.NewRaw(`
DELETE FROM rate_limits WHERE (name, key) IN (
    SELECT name, key FROM rate_limits WHERE tat <= statement_timestamp() ORDER BY tat LIMIT ?)`, limit).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "ratelimit: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "ratelimit: cleanup")
}
