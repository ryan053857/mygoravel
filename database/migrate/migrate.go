// Package migrate 管理 SQL 迁移：构造 goose 迁移器、新建迁移文件、
// 把框架各包内嵌的迁移发布到应用的迁移目录，并检查已发布的迁移没有被改动。
package migrate

import (
	"context"
	"io/fs"

	"github.com/cockroachdb/errors"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/uptrace/bun"
)

// NewProvider 装配 goose 迁移执行器。fsys 根目录为应用线性 SQL 迁移；禁用全局 goose 注册机制。
// 迁移持有 PostgreSQL 会话锁，多个实例同时迁移时串行执行。
func NewProvider(db *bun.DB, fsys fs.FS) (*goose.Provider, error) {
	if db == nil {
		return nil, errors.New("migrate: nil db")
	}
	if fsys == nil {
		return nil, errors.New("migrate: nil fs")
	}
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, 300))
	if err != nil {
		return nil, errors.Wrap(err, "migrate")
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db.DB, fsys,
		goose.WithDisableGlobalRegistry(true), goose.WithSessionLocker(locker))
	if err != nil {
		return nil, errors.Wrap(err, "migrate")
	}
	return p, nil
}

// Up 执行全部未应用迁移；诊断由调用方负责。
func Up(ctx context.Context, p *goose.Provider) error {
	_, err := p.Up(ctx)
	if err != nil {
		return errors.Wrap(err, "migrate: up")
	}
	return nil
}
