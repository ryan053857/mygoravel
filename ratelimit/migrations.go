package ratelimit

import (
	"embed"
	"io/fs"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

// Migrations 返回 Database 使用的 rate_limits 表的 PostgreSQL 迁移，供应用发布。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations/postgres")
	if err != nil {
		panic(err)
	}
	return f
}
