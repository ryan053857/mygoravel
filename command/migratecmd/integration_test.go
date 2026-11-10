//go:build integration

package migratecmd_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
)

func TestMigrateCommandsOnPostgres(t *testing.T) {
	db := dbtest.Open(t, database.Postgres, nil)
	var name string
	if err := db.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(dbtest.RequireDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + name
	path := writeConfig(t, "database:\n  driver: postgres\n  dsn: "+dsn.String()+"\n")
	testMigrateCommands(t, path)
}
