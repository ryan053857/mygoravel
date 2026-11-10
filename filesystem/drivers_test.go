package filesystem_test

import (
	"io/fs"
	"testing"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/filesystem"
)

// testDrivers 是存储测试运行的数据库。SQLite 不需要外部服务，总是运行；
// 带 integration 标签时追加 PostgreSQL，见 drivers_integration_test.go。
var testDrivers = []database.Driver{database.SQLite}

// forEachDriver 在每个 testDrivers 上以子测试运行 test。
func forEachDriver(t *testing.T, test func(t *testing.T, driver database.Driver)) {
	t.Helper()
	for _, driver := range testDrivers {
		t.Run(string(driver), func(t *testing.T) { test(t, driver) })
	}
}

// openDB 打开一个已执行 filesystem 迁移的测试数据库。
func openDB(t *testing.T, driver database.Driver) *bun.DB {
	t.Helper()
	migrations, err := fs.Sub(filesystem.Migrations(), string(driver))
	if err != nil {
		t.Fatal(err)
	}
	return dbtest.Open(t, driver, migrations)
}
