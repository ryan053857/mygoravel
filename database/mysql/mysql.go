// Package mysql 是 MySQL 8.4 驱动（go-sql-driver/mysql），并识别 MySQL 的约束错误。
//
//	db, err := database.Open(ctx, mysql.Driver, cfg.Database)
package mysql

import (
	"database/sql"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	driver "github.com/go-sql-driver/mysql"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/mysqldialect"

	"github.com/ryan053857/mygoravel/database"
)

// Driver 是 MySQL 驱动。DSN 是 URL，例如 mysql://user:pass@localhost:3306/app?tls=true，
// 查询参数原样交给 go-sql-driver/mysql。
//
// 以下设置由驱动固定，DSN 中的同名参数会被覆盖：时间按 UTC 读写（parseTime、loc 与会话 time_zone），
// RowsAffected 返回匹配的行数（clientFoundRows），与 PostgreSQL 和 SQLite 一致。
var Driver database.Driver = mysqlDriver{}

type mysqlDriver struct{}

func (mysqlDriver) Name() string { return "mysql" }

func (mysqlDriver) Connect(dsn string) (*bun.DB, error) {
	cfg, err := parse(dsn)
	if err != nil {
		return nil, err
	}
	connector, err := driver.NewConnector(cfg)
	if err != nil {
		return nil, errors.Wrap(err, "invalid dsn")
	}
	return bun.NewDB(sql.OpenDB(connector), mysqldialect.New(mysqldialect.WithTimeLocation("UTC"))), nil
}

// parse 把 mysql:// URL 转换为驱动配置，并固定时间与 RowsAffected 的设置。
func parse(dsn string) (*driver.Config, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "mysql" || u.Host == "" {
		return nil, errors.New("invalid dsn: want mysql://user:pass@host:3306/database")
	}
	cfg, err := driver.ParseDSN("/?" + u.RawQuery)
	if err != nil {
		return nil, errors.Wrap(err, "invalid dsn")
	}
	cfg.User = u.User.Username()
	cfg.Passwd, _ = u.User.Password()
	cfg.Net, cfg.Addr = "tcp", u.Host
	if u.Port() == "" {
		cfg.Addr = net.JoinHostPort(u.Hostname(), "3306")
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	cfg.ParseTime, cfg.Loc, cfg.ClientFoundRows = true, time.UTC, true
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params["time_zone"] = "'+00:00'"
	return cfg, nil
}

// IsUniqueViolation 报告是否为唯一约束或主键冲突（错误码 1062）。constraints 非空时还要求
// 冲突的约束或索引名匹配其中之一，例如 "articles_slug_key"；主键的名字是 "PRIMARY"。
func IsUniqueViolation(err error, constraints ...string) bool {
	var myErr *driver.MySQLError
	if !errors.As(err, &myErr) || myErr.Number != 1062 {
		return false
	}
	return len(constraints) == 0 || slices.Contains(constraints, uniqueKey(myErr.Message))
}

// uniqueKey 从 "Duplicate entry 'x' for key 'articles.articles_slug_key'" 中取出 "articles_slug_key"。
func uniqueKey(msg string) string {
	i := strings.LastIndex(msg, "for key '")
	if i < 0 {
		return ""
	}
	key := strings.TrimSuffix(msg[i+len("for key '"):], "'")
	if j := strings.LastIndex(key, "."); j >= 0 {
		key = key[j+1:]
	}
	return key
}

// IsDeadlock 报告事务是否因死锁被数据库中止（1213）。InnoDB 已回滚整个事务，可以重试。
func IsDeadlock(err error) bool { return hasNumber(err, 1213) }

// IsForeignKeyViolation 报告是否为外键冲突：插入或更新时引用的行不存在（1452），
// 或删除、更新仍被引用的行（1451）。
func IsForeignKeyViolation(err error) bool { return hasNumber(err, 1451, 1452) }

// IsCheckViolation 报告是否为检查约束冲突（3819）。
func IsCheckViolation(err error) bool { return hasNumber(err, 3819) }

func hasNumber(err error, numbers ...uint16) bool {
	var myErr *driver.MySQLError
	return errors.As(err, &myErr) && slices.Contains(numbers, myErr.Number)
}
