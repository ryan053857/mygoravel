package database

import (
	"context"
	"runtime"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrUnavailable 表示 Open 时数据库连不上。它属于 errorx.Unavailable 分类，
// 命令因此以 command.ExitUnavailable 退出，部署脚本可以据此重试。
var ErrUnavailable = errorx.Define(errorx.Unavailable, "database.unavailable")

// Config 是数据库连接配置。
type Config struct {
	// DSN 是 pgx 支持的连接串，例如 postgres://user:pass@localhost:5432/app?sslmode=disable。
	DSN             string        `yaml:"dsn"`
	MaxOpenConns    int           `yaml:"max_open_conns"`
	MaxIdleConns    int           `yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `yaml:"conn_max_idle_time"`
	// SlowQuery 是慢查询的阈值：执行时间达到它的语句记录一条 Warn 日志，带着把字面量换成 ? 之后的 SQL；0 表示不记录。
	SlowQuery time.Duration `yaml:"slow_query"`
}

// DefaultConfig 返回默认连接池参数；DSN 必须由应用配置。
func DefaultConfig() Config {
	n := 4 * runtime.GOMAXPROCS(0)
	return Config{
		MaxOpenConns:    n,
		MaxIdleConns:    n,
		ConnMaxLifetime: time.Hour,
		ConnMaxIdleTime: 30 * time.Minute,
		SlowQuery:       time.Second,
	}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// Validate 检查 DSN 非空与连接池参数；DSN 的格式在 Open 时检查。
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
	if c.SlowQuery < 0 {
		return errors.New("slow_query must be >= 0")
	}
	return nil
}

// Open 创建 PostgreSQL 连接池并 Ping；任何失败都关闭已打开的资源。Ping 失败时返回 ErrUnavailable。
// cfg.SlowQuery 大于 0 时记录慢查询；WithTelemetry 让每条语句报告指标和链路。
func Open(ctx context.Context, cfg Config, opts ...Option) (*bun.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "database")
	}
	parsed, err := pgx.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.Wrap(err, "database: invalid dsn")
	}
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	db := bun.NewDB(stdlib.OpenDB(*parsed), pgdialect.New())
	if h := newQueryHook(o, cfg, parsed.Database, parsed.Host, parsed.Port); h != nil {
		db = db.WithQueryHook(h)
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
