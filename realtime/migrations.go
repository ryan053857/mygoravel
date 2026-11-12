package realtime

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回 DatabasePresence 使用的 realtime_presence 表的迁移，供应用发布。
// 只有使用 DatabasePresence 的应用需要它。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
