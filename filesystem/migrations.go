package filesystem

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Migrations 返回供应用发布的 SQL。服务运行时使用应用已发布、已执行的迁移。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
