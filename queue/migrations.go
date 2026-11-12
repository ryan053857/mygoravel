package queue

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回队列的迁移，供应用发布：queue_scheduled 与 queue_failed_jobs 表，以及转发任务的逻辑复制槽。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
