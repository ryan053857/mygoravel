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
// 每个 key 只存一个理论到达时间（tat），一次 Allow 是一条原子的 upsert。
// PostgreSQL 取数据库时钟；SQLite 只在单机上使用，取应用时钟，tat 存为 Unix 微秒。
//
// 支持 PostgreSQL 和 SQLite。每次 Allow 都是一次写入，适合登录、验证码、发邮件这类低频操作；
// 每个请求都要经过的全局限流请使用 Memory。
type Database struct {
	gcra
	db     bun.IDB
	name   string
	sqlite bool
}

// NewDatabase 创建数据库限流器。name 区分共用 rate_limits 表的不同限流器，
// 同名的限流器共享配额，因此 name 应与 cfg 一一对应。
// db 必须已执行 rate_limits 迁移；db 为 nil、name 为空、cfg 不合法或数据库不是 PostgreSQL 或 SQLite 时 panic。
func NewDatabase(db bun.IDB, name string, cfg Config) *Database {
	switch {
	case db == nil:
		panic("ratelimit: NewDatabase: nil database")
	case name == "":
		panic("ratelimit: NewDatabase: empty name")
	case db.Dialect().Name() != dialect.PG && db.Dialect().Name() != dialect.SQLite:
		panic("ratelimit: NewDatabase: only PostgreSQL and SQLite are supported")
	}
	return &Database{gcra: newGCRA("NewDatabase", cfg), db: db, name: name, sqlite: db.Dialect().Name() == dialect.SQLite}
}

// Allow 实现 Limiter。
func (d *Database) Allow(ctx context.Context, key string) error {
	if d.sqlite {
		return d.allowSQLite(ctx, key)
	}
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
	return d.exceeded(wait)
}

// allowSQLite 与 PostgreSQL 的逻辑相同，tat 与当前时间都是应用时钟的 Unix 微秒。
//
// 当前时间必须在取得写锁之后读取：先读时间再排队等锁，时间较早的调用可能后执行，
// 看到被其他调用推后的 tat，从而被多算几微秒、错误地拒绝。写事务在 BEGIN 时取得写锁
// （database 包为 SQLite 设置了 _txlock=immediate），因此在事务内读取时间，
// 执行顺序与时间顺序一致。
func (d *Database) allowSQLite(ctx context.Context, key string) error {
	step, ahead := d.step.Microseconds(), d.ahead.Microseconds()
	var allowed bool
	var wait int64
	err := d.db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		now := time.Now().UnixMicro()
		err := tx.NewRaw(`
INSERT INTO rate_limits AS r (name, key, tat) VALUES (?, ?, ?)
ON CONFLICT (name, key) DO UPDATE
   SET tat = max(r.tat, ?) + ?
 WHERE r.tat <= ?
RETURNING true`,
			d.name, key, now+step, now, step, now+ahead).Scan(ctx, &allowed)
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		err = tx.NewRaw(`SELECT tat - ? FROM rate_limits WHERE name = ? AND key = ?`, now+ahead, d.name, key).Scan(ctx, &wait)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	})
	switch {
	case err != nil:
		return errors.Wrap(err, "ratelimit: allow")
	case allowed:
		return nil
	}
	return d.exceeded(wait)
}

// exceeded 把以微秒计的等待时间转换为 ErrExceeded。
func (d *Database) exceeded(wait int64) error {
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
	// SQLite 的 tat 是应用时钟的 Unix 微秒。
	var now any = bun.Safe("statement_timestamp()")
	if d.sqlite {
		now = time.Now().UnixMicro()
	}
	res, err := d.db.NewRaw(`
DELETE FROM rate_limits WHERE (name, key) IN (
    SELECT name, key FROM rate_limits WHERE tat <= ? ORDER BY tat LIMIT ?)`, now, limit).Exec(ctx)
	if err != nil {
		return 0, errors.Wrap(err, "ratelimit: cleanup")
	}
	n, err := res.RowsAffected()
	return int(n), errors.Wrap(err, "ratelimit: cleanup")
}
