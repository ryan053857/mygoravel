//go:build integration

package queue_test

import "github.com/ryan053857/mygoravel/database/postgres"

func init() { testDrivers = append(testDrivers, postgres.Driver) }
