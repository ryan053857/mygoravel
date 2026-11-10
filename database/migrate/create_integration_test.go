//go:build integration

package migrate_test

import (
	"testing"

	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
)

func TestCreatedStubsRunOnPostgres(t *testing.T) {
	testStubsRun(t, postgres.Driver)
}

func TestCreatedStubsRunOnMySQL(t *testing.T) {
	testStubsRun(t, mysql.Driver)
}
