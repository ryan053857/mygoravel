package exports

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回导出表 exports 的迁移，供应用发布。
// 它引用 operation 包的 operations 表，发布时排在 operation.Migrations() 之后。
// 根目录下按数据库类型分为 postgres、mysql 和 sqlite 三个目录，由 migrate.Publish 按应用的数据库选择。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
