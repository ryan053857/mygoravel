package filesystem

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回files 表的迁移，供应用发布。服务运行时使用应用已发布、已执行的迁移。
// 根目录下按数据库类型分为 postgres 和 sqlite 两个目录，由 migrate.Publish 按应用的数据库选择。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
