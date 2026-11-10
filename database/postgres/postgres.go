// Package postgres 是 PostgreSQL 驱动（pgx），并识别 PostgreSQL 的约束错误。
//
//	db, err := database.Open(ctx, postgres.Driver, cfg.Database)
package postgres

import (
	"slices"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/ryan053857/mygoravel/database"
)

// Driver 是 PostgreSQL 驱动。DSN 是 pgx 支持的连接串，例如 postgres://user:pass@localhost:5432/app?sslmode=disable。
var Driver database.Driver = driver{}

type driver struct{}

func (driver) Name() string { return "postgres" }

func (driver) Connect(dsn string) (*bun.DB, error) {
	parsed, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.Wrap(err, "invalid dsn")
	}
	return bun.NewDB(stdlib.OpenDB(*parsed), pgdialect.New()), nil
}

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

func hasCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
