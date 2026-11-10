//go:build integration

package lock_test

import "github.com/ryan053857/mygoravel/database"

func init() { testDrivers = append(testDrivers, database.Postgres) }
