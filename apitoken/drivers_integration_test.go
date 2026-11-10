//go:build integration

package apitoken_test

import (
	"github.com/ryan053857/mygoravel/database/postgres"
)

func init() { testDrivers = append(testDrivers, postgres.Driver) }
