package login

import (
	"embed"
	"io/fs"
)

//go:embed migrations
var migrationFiles embed.FS

// Migrations 返回 Realm 使用的 auth_identifiers、auth_passwords、auth_challenges 表的迁移，供应用发布。
// 默认的限流器还需要 ratelimit.Migrations。
func Migrations() fs.FS {
	f, err := fs.Sub(migrationFiles, "migrations")
	if err != nil {
		panic(err)
	}
	return f
}
