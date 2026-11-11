package database

import (
	"context"
	"runtime"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrUnavailable 表示 Open 时数据库连不上。它属于 errorx.Unavailable 分类，
// 命令因此以 command.ExitUnavailable 退出，部署脚本可以据此重试。
var ErrUnavailable = errorx.Define(errorx.Unavailable, "database.unavailable")

// Driver 是一种数据库的驱动，由子包 database/postgres 和 database/sqlite 提供。
// 应用只链接自己 import 的驱动；迁移是按数据库写的，因此数据库类型在代码中选定，而不是在配置中。
type Driver interface {
	// Name 是数据库类型，也是框架迁移的目录名，例如 "postgres"、"sqlite"。
	Name() string
	// Connect 检查 dsn 并返回尚未建立连接的 *bun.DB。
	Connect(dsn string) (*bun.DB, error)
}

// Config 是数据库连接配置。
type Config struct {
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
}

// DefaultConfig 返回默认连接池参数；DSN 必须由应用配置。
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

// Validate 检查 DSN 非空与连接池参数；DSN 的格式由驱动在 Open 时检查。
func (c Config) Validate() error {
	if c.DSN == "" {
		return errors.New("dsn is required")
	}
	if c.MaxOpenConns < 0 || c.MaxIdleConns < 0 {
		return errors.New("connection pool sizes must be >= 0")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 {
		return errors.New("connection lifetimes must be >= 0")
	}
	return nil
}

// Open 用 driver 创建连接池并 Ping；任何失败都关闭已打开的资源。Ping 失败时返回 ErrUnavailable。
func Open(ctx context.Context, driver Driver, cfg Config) (*bun.DB, error) {
	if driver == nil {
		return nil, errors.New("database: nil driver")
	}
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "database")
	}
	db, err := driver.Connect(cfg.DSN)
	if err != nil {
		return nil, errors.Wrapf(err, "database: %s", driver.Name())
	}
	db.SetMaxOpenConns(cfg.MaxOpenConns)
	db.SetMaxIdleConns(cfg.MaxIdleConns)
	db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, ErrUnavailable.Wrap(errors.Wrap(err, "database: ping"))
	}
	return db, nil
}
