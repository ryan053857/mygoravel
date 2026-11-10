//go:build integration

package lock_test

import (
	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
)

func init() { testDrivers = append(testDrivers, postgres.Driver, mysql.Driver) }
