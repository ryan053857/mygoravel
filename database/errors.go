package database

import (
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation 报告是否为唯一约束或主键冲突。constraints 非空时还要求冲突的约束匹配其中之一：
// PostgreSQL 按约束名匹配，例如 "articles_slug_key"；SQLite 的错误中没有约束名，
// 按它报告的列匹配，例如 "articles.slug" 或 "files.disk, files.key"，表达式索引则按索引名匹配。
func IsUniqueViolation(err error, constraints ...string) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505" && (len(constraints) == 0 || slices.Contains(constraints, pgErr.ConstraintName))
	}
	var liteErr *sqlite.Error
	if errors.As(err, &liteErr) {
		code := liteErr.Code()
		if code != sqlite3.SQLITE_CONSTRAINT_UNIQUE && code != sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY {
			return false
		}
		return len(constraints) == 0 || slices.Contains(constraints, sqliteUniqueTarget(liteErr.Error()))
	}
	return false
}

// sqliteUniqueTarget 从 "... UNIQUE constraint failed: articles.slug (2067)" 中取出 "articles.slug"；
// 表达式索引的 "index 'articles_lower_slug'" 取出索引名。
func sqliteUniqueTarget(msg string) string {
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
func IsForeignKeyViolation(err error) bool {
	return hasCode(err, "23503", sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY)
}

// IsCheckViolation 报告是否为检查约束冲突。
func IsCheckViolation(err error) bool {
	return hasCode(err, "23514", sqlite3.SQLITE_CONSTRAINT_CHECK)
}

func hasCode(err error, pgCode string, sqliteCode int) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == pgCode
	}
	var liteErr *sqlite.Error
	return errors.As(err, &liteErr) && liteErr.Code() == sqliteCode
}
