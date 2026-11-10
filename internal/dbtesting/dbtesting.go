// Package dbtesting 让框架中依赖数据库的测试在每种数据库上运行。
//
// SQLite 不需要外部服务，总是运行；带 integration 构建标签时追加 PostgreSQL 和 MySQL，
// 它们的服务器地址见 dbtest.EnvURL。新增一种数据库只需要修改本包。
package dbtesting

import (
	"io/fs"
	"testing"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/sqlite"
)

// drivers 是测试运行的数据库，integration 标签下由 drivers_integration.go 追加。
var drivers = []database.Driver{sqlite.Driver}

// Drivers 返回测试运行的数据库。
func Drivers() []database.Driver { return drivers }

// ForEach 在每种数据库上以子测试运行 test，子测试以数据库名命名。
func ForEach(t *testing.T, test func(t *testing.T, driver database.Driver)) {
	t.Helper()
	for _, driver := range drivers {
		t.Run(driver.Name(), func(t *testing.T) { test(t, driver) })
	}
}

// Open 为当前测试打开 driver 的临时数据库，并执行 migrations 中该数据库的迁移。
// migrations 的根目录按数据库类型分目录，与框架各包的 Migrations() 相同；nil 时打开空库。
func Open(t testing.TB, driver database.Driver, migrations fs.FS) *bun.DB {
	t.Helper()
	if migrations == nil {
		return dbtest.Open(t, driver, nil)
	}
	sub, err := fs.Sub(migrations, driver.Name())
	if err != nil {
		t.Fatal(err)
	}
	return dbtest.Open(t, driver, sub)
}
