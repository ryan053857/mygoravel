//go:build integration

package migratecmd_test

import (
	"context"
	"net/url"
	"testing"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/postgres"
)

func TestMigrateCommandsOnPostgres(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, nil)
	var name string
	if err := db.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(dbtest.RequireDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + name
	path := writeConfig(t, "database:\n  dsn: "+dsn.String()+"\n")
	testMigrateCommands(t, postgres.Driver, path)
}
