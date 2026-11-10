package database

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/dialect/sqlitedialect"
	_ "modernc.org/sqlite" // 注册 database/sql 驱动 "sqlite"
)

// Driver 是数据库类型。
type Driver string

const (
	Postgres Driver = "postgres"
	SQLite   Driver = "sqlite"
)

// sqliteParams 是每个 SQLite 连接的参数：WAL 让读写互不阻塞；busy_timeout 让写入排队等待；
// SQLite 默认不检查外键；写事务在 BEGIN 时就取得写锁（_txlock=immediate），
// 避免先读后写的事务在升级锁时直接失败。
const sqliteParams = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)" +
	"&_pragma=synchronous(NORMAL)&_txlock=immediate"

// Config 是数据库连接配置。Driver 为 SQLite 时，DSN 是数据库文件的路径。
type Config struct {
	Driver          Driver        `yaml:"driver"`
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// DefaultConfig 返回默认连接池参数；Driver 和 DSN 必须由应用配置。
func DefaultConfig() Config {
	n := 4 * runtime.GOMAXPROCS(0)
	return Config{
		MaxOpenConns:    n,
		MaxIdleConns:    n,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 30 * time.Minute,
	}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查 Driver、DSN 与连接池参数。
func (c Config) Validate() error {
	_, err := c.connector()
	return err
}

// connector 校验配置并返回打开连接池的函数，Validate 与 Open 共用同一套检查。
func (c Config) connector() (func() (*bun.DB, error), error) {
	if c.DSN == "" {
		return nil, errors.New("dsn is required")
	}
	if c.MaxOpenConns < 0 || c.MaxIdleConns < 0 {
		return nil, errors.New("connection pool sizes must be >= 0")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 {
		return nil, errors.New("connection lifetimes must be >= 0")
	}
	switch c.Driver {
	case Postgres:
		parsed, err := pgx.ParseConfig(c.DSN)
		if err != nil {
			return nil, errors.Wrap(err, "invalid dsn")
		}
		return func() (*bun.DB, error) {
			return bun.NewDB(stdlib.OpenDB(*parsed), pgdialect.New()), nil
		}, nil
	case SQLite:
		if c.DSN == ":memory:" || strings.HasPrefix(c.DSN, "file:") || strings.Contains(c.DSN, "?") {
			return nil, errors.New("sqlite dsn must be a file path; in-memory databases and connection parameters are not supported")
		}
		return func() (*bun.DB, error) {
			// SQLite 不会创建缺失的目录，提前报出比 "unable to open database file" 更清楚的错误。
			if _, err := os.Stat(filepath.Dir(c.DSN)); err != nil {
				return nil, errors.Wrap(err, "sqlite directory")
			}
			sqldb, err := sql.Open("sqlite", c.DSN+sqliteParams)
			if err != nil {
				return nil, err
			}
			return bun.NewDB(sqldb, sqlitedialect.New()), nil
		}, nil
	case "":
		return nil, errors.New("driver is required (postgres or sqlite)")
	default:
		return nil, errors.Newf("unsupported driver %q (postgres or sqlite)", c.Driver)
	}
}

// Open 创建原生连接池并 Ping；任何失败都关闭已打开的资源。
func Open(ctx context.Context, cfg Config) (*bun.DB, error) {
	open, err := cfg.connector()
	if err != nil {
		return nil, errors.Wrap(err, "database")
	}
	db, err := open()
	if err != nil {
		return nil, errors.Wrap(err, "database")
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.Wrap(err, "database: ping")
	}
	return db, nil
}
