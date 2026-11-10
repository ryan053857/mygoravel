package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// EnvDSN 是 PostgreSQL 测试管理员连接的环境变量。
const EnvDSN = "DATABASE_TEST_DSN"

// RequireDSN 返回 PostgreSQL 测试管理员 DSN，未设置时使测试失败。
func RequireDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(EnvDSN)
	if dsn == "" {
		t.Fatalf("%s is required for this test", EnvDSN)
	}
	return dsn
}

// Open 为当前测试创建一个空的临时数据库，migrations 非 nil 时执行迁移，返回 *bun.DB；测试结束时删除。
// PostgreSQL 在 DATABASE_TEST_DSN 指向的服务器上建库；SQLite 在 t.TempDir() 中建文件，不需要任何外部服务。
func Open(t testing.TB, driver database.Driver, migrations fs.FS) *bun.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var db *bun.DB
	switch driver {
	case database.Postgres:
		db = openPostgres(ctx, t)
	case database.SQLite:
		var err error
		db, err = database.Open(ctx, database.Config{Driver: database.SQLite, DSN: filepath.Join(t.TempDir(), "test.db")})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := db.Close(); err != nil {
				t.Errorf("dbtest: close database: %v", err)
			}
		})
	default:
		t.Fatalf("dbtest: unsupported driver %q", driver)
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

func openPostgres(ctx context.Context, t testing.TB) *bun.DB {
	t.Helper()
	adminDSN := RequireDSN(t)
	admin, err := database.Open(ctx, database.Config{Driver: database.Postgres, DSN: adminDSN})
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
		db, err := database.Open(cleanCtx, database.Config{Driver: database.Postgres, DSN: adminDSN})
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

func randomName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
