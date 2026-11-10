package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// EnvURL 返回 driver 的测试服务器管理员连接所在的环境变量，例如 POSTGRES_TEST_URL。
func EnvURL(driver database.Driver) string {
	return strings.ToUpper(driver.Name()) + "_TEST_URL"
}

// RequireURL 返回 driver 的测试服务器管理员连接，环境变量未设置时使测试失败。
func RequireURL(t testing.TB, driver database.Driver) string {
	t.Helper()
	url := os.Getenv(EnvURL(driver))
	if url == "" {
		t.Fatalf("%s is required for this test", EnvURL(driver))
	}
	return url
}

// Open 用 driver 为当前测试创建一个空的临时数据库，migrations 非 nil 时执行迁移，返回 *bun.DB；测试结束时删除。
// PostgreSQL 和 MySQL 在 POSTGRES_TEST_URL、MYSQL_TEST_URL 指向的服务器上建库；
// SQLite 在 t.TempDir() 中建文件，不需要任何外部服务。
func Open(t testing.TB, driver database.Driver, migrations fs.FS) *bun.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if driver == nil {
		t.Fatal("dbtest: nil driver")
	}
	var db *bun.DB
	switch driver.Name() {
	case "postgres":
		db = openPostgres(ctx, t, driver)
	case "mysql":
		db = openMySQL(ctx, t, driver)
	case "sqlite":
		var err error
		db, err = database.Open(ctx, driver, database.Config{DSN: filepath.Join(t.TempDir(), "test.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Errorf("dbtest: close database: %v", err)
			}
		})
	default:
		t.Fatalf("dbtest: unsupported driver %q", driver.Name())
	}

	if migrations != nil {
		p, err := migrate.NewProvider(db, migrations)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Up(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func openPostgres(ctx context.Context, t testing.TB, driver database.Driver) *bun.DB {
	t.Helper()
	adminDSN := RequireURL(t, driver)
	admin, err := database.Open(ctx, driver, database.Config{DSN: adminDSN})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	name := "test_" + randomName()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := database.Open(cleanCtx, driver, database.Config{DSN: adminDSN})
		if err != nil {
			t.Errorf("dbtest: reopen admin: %v", err)
			return
		}
		defer db.Close()
		if _, err := db.ExecContext(cleanCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("dbtest: drop database: %v", err)
		}
	})

	pgxConfig, err := pgx.ParseConfig(adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	pgxConfig.Database = name
	db := bun.NewDB(stdlib.OpenDB(*pgxConfig), pgdialect.New())
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("dbtest: close database: %v", err)
		}
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func openMySQL(ctx context.Context, t testing.TB, driver database.Driver) *bun.DB {
	t.Helper()
	adminURL := RequireURL(t, driver)
	admin, err := database.Open(ctx, driver, database.Config{DSN: adminURL})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()

	name := "test_" + randomName()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := database.Open(cleanCtx, driver, database.Config{DSN: adminURL})
		if err != nil {
			t.Errorf("dbtest: reopen admin: %v", err)
			return
		}
		defer db.Close()
		if _, err := db.ExecContext(cleanCtx, "DROP DATABASE IF EXISTS "+name); err != nil {
			t.Errorf("dbtest: drop database: %v", err)
		}
	})

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := database.Open(ctx, driver, database.Config{DSN: u.String()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("dbtest: close database: %v", err)
		}
	})
	return db
}

func randomName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
