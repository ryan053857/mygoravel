//go:build integration

package migratecmd_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
)

func TestMigrateCommandsOnPostgres(t *testing.T) {
	testMigrateCommands(t, postgres.Driver, serverConfig(t, postgres.Driver, "SELECT current_database()"))
}

func TestMigrateCommandsOnMySQL(t *testing.T) {
	testMigrateCommands(t, mysql.Driver, serverConfig(t, mysql.Driver, "SELECT DATABASE()"))
}

// serverConfig 创建临时数据库，返回指向它的配置文件路径；current 查询当前库名。
func serverConfig(t *testing.T, driver database.Driver, current string) string {
	db := dbtest.Open(t, driver, nil)
	var name string
	if err := db.QueryRowContext(context.Background(), current).Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(dbtest.RequireURL(t, driver))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + name
	return writeConfig(t, "database:\n  dsn: "+dsn.String()+"\n")
}
