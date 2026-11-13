package database

import (
	"context"
	"database/sql"
	"math"
	"runtime"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/go-tenon/tenon/errorx"
)

// ErrUnavailable 表示 Open 时数据库连不上。它属于 errorx.Unavailable 分类，
// 命令因此以 command.ExitUnavailable 退出，部署脚本可以据此重试。
var ErrUnavailable = errorx.Define(errorx.Unavailable, "database.unavailable")

// Config 是数据库连接配置。连接池由 pgxpool 管理；MaxConns、MaxConnLifetime、MaxConnIdleTime 为 0 时使用 DefaultConfig 的值。
type Config struct {
	// DSN 是 pgx 支持的连接串，例如 postgres://user:pass@localhost:5432/app?sslmode=disable。
	DSN string `yaml:"dsn"`
	// MaxConns 是连接池的最大连接数。所有实例、所有进程的连接数之和不应超过数据库或 PgBouncer 的上限。
	MaxConns int `yaml:"max_conns"`
	// MinConns 是连接池保持的最少连接数，空闲时也不关闭。
	MinConns int `yaml:"min_conns"`
	// MaxConnLifetime 是连接的最长存活时间，到期后在空闲时关闭，例如让数据库的配置变更和负载均衡生效。
	MaxConnLifetime time.Duration `yaml:"max_conn_lifetime"`
	// MaxConnIdleTime 是空闲连接的最长保留时间，超过 MinConns 的空闲连接到期后关闭。
	MaxConnIdleTime time.Duration `yaml:"max_conn_idle_time"`
	// SlowQuery 是慢查询的阈值：执行时间达到它的语句记录一条 Warn 日志，带着把字面量换成 ? 之后的 SQL；0 表示不记录。
	SlowQuery time.Duration `yaml:"slow_query"`
}

// DefaultConfig 返回默认连接池参数；DSN 必须由应用配置。
func DefaultConfig() Config {
	return Config{
		MaxConns:        4 * runtime.GOMAXPROCS(0),
		MaxConnLifetime: time.Hour,
		MaxConnIdleTime: 30 * time.Minute,
		SlowQuery:       time.Second,
	}
}

// SetDefaults 实现 config.Defaulter，使 config.Load 自动填充默认值。
func (c *Config) SetDefaults() { *c = DefaultConfig() }

// withDefaults 返回把为 0 的连接池参数换成默认值之后的配置。
func (c Config) withDefaults() Config {
	d := DefaultConfig()
	if c.MaxConns == 0 {
		c.MaxConns = d.MaxConns
	}
	if c.MaxConnLifetime == 0 {
		c.MaxConnLifetime = d.MaxConnLifetime
	}
	if c.MaxConnIdleTime == 0 {
		c.MaxConnIdleTime = d.MaxConnIdleTime
	}
	return c
}

// Validate 检查 DSN 非空与连接池参数；DSN 的格式在 Open 时检查。
func (c Config) Validate() error {
	if c.DSN == "" {
		return errors.New("dsn is required")
	}
	c = c.withDefaults()
	if c.MaxConns < 1 || c.MaxConns > math.MaxInt32 {
		return errors.New("max_conns must be positive")
	}
	if c.MinConns < 0 || c.MinConns > c.MaxConns {
		return errors.New("min_conns must be between 0 and max_conns")
	}
	if c.MaxConnLifetime < 0 || c.MaxConnIdleTime < 0 {
		return errors.New("max_conn_lifetime and max_conn_idle_time must not be negative")
	}
	if c.SlowQuery < 0 {
		return errors.New("slow_query must be >= 0")
	}
	return nil
}

// Open 创建 PostgreSQL 连接池并 Ping；任何失败都关闭已打开的资源。Ping 失败时返回 ErrUnavailable。
// cfg.SlowQuery 大于 0 时记录慢查询；WithTelemetry 让每条语句报告指标和链路，并报告连接池的指标。
//
// 连接池是 pgxpool，返回的 *bun.DB 每次执行语句或开始事务时从它取一个连接，用完立即放回。
// 取连接时等待的时长由 ctx 决定，acquire.WithTimeout 可以只给这一步设一个较短的时限，超过时返回 ErrBusy。
func Open(ctx context.Context, cfg Config, opts ...Option) (*bun.DB, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "database")
	}
	cfg = cfg.withDefaults()
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN)
	if err != nil {
		return nil, errors.Wrap(err, "database: invalid dsn")
	}
	o := options{name: "primary"}
	for _, opt := range opts {
		opt(&o)
	}
	poolCfg.MaxConns, poolCfg.MinConns = int32(cfg.MaxConns), int32(cfg.MinConns)
	poolCfg.MaxConnLifetime, poolCfg.MaxConnIdleTime = cfg.MaxConnLifetime, cfg.MaxConnIdleTime
	if o.readOnly {
		poolCfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, errors.Wrap(err, "database: create pool")
	}
	c := newConnector(pool, o)
	sqlDB := sql.OpenDB(c) // Close 时关闭 c，从而关闭 pool
	// 连接由 pool 管理：database/sql 不保留空闲连接，用完立即放回 pool，也不限制数量。
	sqlDB.SetMaxIdleConns(0)
	db := bun.NewDB(sqlDB, pgdialect.New())
	conn := poolCfg.ConnConfig
	if h := newQueryHook(o, cfg, conn.Database, conn.Host, conn.Port); h != nil {
		db = db.WithQueryHook(h)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, ErrUnavailable.Wrap(errors.Wrap(err, "database: ping"))
	}
	return db, nil
}
