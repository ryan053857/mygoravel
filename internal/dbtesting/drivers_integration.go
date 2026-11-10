//go:build integration

package dbtesting

import (
	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
)

func init() { drivers = append(drivers, postgres.Driver, mysql.Driver) }
