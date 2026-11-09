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
)

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

// Validate 检查 DSN 与连接池参数。
func (c Config) Validate() error {
	_, err := c.parse()
	return err
}

func (c Config) parse() (*pgx.ConnConfig, error) {
	if c.DSN == "" {
		return nil, errors.New("dsn is required")
	}
	if c.MaxOpenConns < 0 || c.MaxIdleConns < 0 {
		return nil, errors.New("connection pool sizes must be >= 0")
	}
	if c.ConnMaxLifetime < 0 || c.ConnMaxIdleTime < 0 {
		return nil, errors.New("connection lifetimes must be >= 0")
	}
	parsed, err := pgx.ParseConfig(c.DSN)
	if err != nil {
		return nil, errors.Wrap(err, "invalid dsn")
	}
	return parsed, nil
}

// Open 创建原生连接池并 Ping；任何失败都关闭已打开的资源。
func Open(ctx context.Context, cfg Config) (*bun.DB, error) {
	pgxCfg, err := cfg.parse()
	if err != nil {
		return nil, errors.Wrap(err, "database")
	}
	sqldb := stdlib.OpenDB(*pgxCfg)
	sqldb.SetMaxOpenConns(cfg.MaxOpenConns)
	sqldb.SetMaxIdleConns(cfg.MaxIdleConns)
	sqldb.SetConnMaxLifetime(cfg.ConnMaxLifetime)
	sqldb.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
	db := bun.NewDB(sqldb, pgdialect.New())
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.Wrap(err, "database: ping")
	}
	return db, nil
}
