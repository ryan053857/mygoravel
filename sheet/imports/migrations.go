package imports

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回导入表 imports 和失败行表 import_failures 的迁移，供应用发布。
// 它们引用 operation 包的 operations 表，发布时排在 operation.Migrations() 之后。
// 根目录下按数据库类型分为 postgres、mysql 和 sqlite 三个目录，由 migrate.Publish 按应用的数据库选择。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
