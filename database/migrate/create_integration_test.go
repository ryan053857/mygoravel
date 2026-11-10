//go:build integration

package migrate_test

import (
	"testing"

	"github.com/ryan053857/mygoravel/database"
)

func TestCreatedStubsRunOnPostgres(t *testing.T) {
	testStubsRun(t, database.Postgres)
}
