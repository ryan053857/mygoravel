package queue

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回 queue_jobs 与 queue_failed_jobs 表的迁移，供应用发布。
// 根目录下按数据库类型分为 postgres 和 sqlite 两个目录，由 migrate.Publish 按应用的数据库选择。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
