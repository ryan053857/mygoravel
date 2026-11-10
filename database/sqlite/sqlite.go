// Package sqlite 是 SQLite 驱动（纯 Go 的 modernc.org/sqlite，不需要 cgo），并识别 SQLite 的约束错误。
//
//	db, err := database.Open(ctx, sqlite.Driver, cfg.Database)
package sqlite

import (
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"

	"github.com/ryan053857/mygoravel/database"
)

// Driver 是 SQLite 驱动。DSN 是数据库文件的路径，所在目录必须已经存在。
var Driver database.Driver = driver{}

// params 是每个连接的参数：WAL 让读写互不阻塞；busy_timeout 让写入排队等待；
// SQLite 默认不检查外键；写事务在 BEGIN 时就取得写锁（_txlock=immediate），
// 避免先读后写的事务在升级锁时直接失败。
const params = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)&_txlock=immediate"

type driver struct{}

func (driver) Name() string { return "sqlite" }

func (driver) Connect(dsn string) (*bun.DB, error) {
	if dsn == ":memory:" || strings.HasPrefix(dsn, "file:") || strings.Contains(dsn, "?") {
		return nil, errors.New("dsn must be a file path; in-memory databases and connection parameters are not supported")
	}
	// SQLite 不会创建缺失的目录，提前报出比 "unable to open database file" 更清楚的错误。
	if _, err := os.Stat(filepath.Dir(dsn)); err != nil {
		return nil, errors.Wrap(err, "directory")
	}
	sqldb, err := sql.Open("sqlite", dsn+params)
	if err != nil {
		return nil, err
	}
	return bun.NewDB(sqldb, sqlitedialect.New()), nil
}

// IsUniqueViolation 报告是否为唯一约束或主键冲突。SQLite 的错误中没有约束名，
// constraints 非空时按它报告的列匹配，例如 "articles.slug" 或 "files.disk, files.key"；
// 表达式索引按索引名匹配。
func IsUniqueViolation(err error, constraints ...string) bool {
	var liteErr *sqlite.Error
	if !errors.As(err, &liteErr) {
		return false
	}
	if code := liteErr.Code(); code != sqlite3.SQLITE_CONSTRAINT_UNIQUE && code != sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
		return false
	}
	return len(constraints) == 0 || slices.Contains(constraints, uniqueTarget(liteErr.Error()))
}

// uniqueTarget 从 "... UNIQUE constraint failed: articles.slug (2067)" 中取出 "articles.slug"；
// 表达式索引的 "index 'articles_lower_slug'" 取出索引名。
func uniqueTarget(msg string) string {
	_, target, ok := strings.Cut(msg, "UNIQUE constraint failed: ")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(target, " ("); i >= 0 {
		target = target[:i]
	}
	if name, ok := strings.CutPrefix(target, "index '"); ok {
		return strings.TrimSuffix(name, "'")
	}
	return target
}

// IsForeignKeyViolation 报告是否为外键冲突。
func IsForeignKeyViolation(err error) bool { return hasCode(err, sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY) }

// IsCheckViolation 报告是否为检查约束冲突。
func IsCheckViolation(err error) bool { return hasCode(err, sqlite3.SQLITE_CONSTRAINT_CHECK) }

func hasCode(err error, code int) bool {
	var liteErr *sqlite.Error
	return errors.As(err, &liteErr) && liteErr.Code() == code
}
