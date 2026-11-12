package database

import (
	"slices"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgconn"
)

// IsUniqueViolation 报告是否为唯一约束或主键冲突（SQLSTATE 23505）。
// constraints 非空时还要求约束名匹配其中之一，例如 "articles_slug_key"。
func IsUniqueViolation(err error, constraints ...string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		(len(constraints) == 0 || slices.Contains(constraints, pgErr.ConstraintName))
}

// IsDeadlock 报告事务是否因死锁被数据库中止（SQLSTATE 40P01）。整个事务可以重试。
func IsDeadlock(err error) bool { return hasCode(err, "40P01") }

// IsForeignKeyViolation 报告是否为外键冲突（SQLSTATE 23503）。
func IsForeignKeyViolation(err error) bool { return hasCode(err, "23503") }

// IsCheckViolation 报告是否为检查约束冲突（SQLSTATE 23514）。
func IsCheckViolation(err error) bool { return hasCode(err, "23514") }

// IsReadOnly 报告是否因为在只读事务中写入而失败（SQLSTATE 25006），例如通过 ReadOnly 的连接池写入只读副本。
func IsReadOnly(err error) bool { return hasCode(err, "25006") }

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
