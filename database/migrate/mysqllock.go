package migrate

import (
	"context"
	"database/sql"

	"github.com/cockroachdb/errors"
)

// mysqlLockTimeout 是等待其他实例完成迁移的最长秒数，与 PostgreSQL 会话锁一致。
const mysqlLockTimeout = 300

// mysqlLocker 用 GET_LOCK 实现 goose 的会话锁：锁属于连接，连接断开时自动释放，
// 与 PostgreSQL 的 advisory lock 相同，不需要额外的锁表。锁名包含库名，同一服务器上的不同应用互不阻塞。
type mysqlLocker struct{}

func (mysqlLocker) SessionLock(ctx context.Context, conn *sql.Conn) error {
	var got sql.NullInt64
	err := conn.QueryRowContext(ctx, "SELECT GET_LOCK(CONCAT('goose:', DATABASE()), ?)", mysqlLockTimeout).Scan(&got)
	switch {
	case err != nil:
		return errors.Wrap(err, "migrate: acquire mysql lock")
	case !got.Valid || got.Int64 != 1:
		return errors.Newf("migrate: timed out after %ds waiting for another migration", mysqlLockTimeout)
	}
	return nil
}

func (mysqlLocker) SessionUnlock(ctx context.Context, conn *sql.Conn) error {
	_, err := conn.ExecContext(ctx, "DO RELEASE_LOCK(CONCAT('goose:', DATABASE()))")
	return errors.Wrap(err, "migrate: release mysql lock")
}
