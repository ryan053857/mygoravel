package ai

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回保存对话的表的迁移，供应用发布。根目录下按数据库类型分为 postgres、mysql 和 sqlite 三个目录，
// 分别由 ai/postgres、ai/mysql 和 ai/sqlite 的 NewSessions 使用。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
