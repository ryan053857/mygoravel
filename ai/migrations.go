package ai

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回保存对话的表的迁移，供应用发布。目前只有 postgres 目录，见 ai/postgres。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
