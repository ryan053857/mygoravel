package lock

import (
	"embed"
	"io/fs"
)

//go:embed migrations/postgres/*.sql
var migrationFiles embed.FS

// Migrations 返回 DBStore 使用的 locks 表的 PostgreSQL 迁移，供应用发布。
// 迁移按方言放在 migrations/<dialect> 下，MySQL 与 SQLite 随数据库多方言支持一起提供。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations/postgres")
	if err != nil {
		panic(err)
	}
	return f
}
