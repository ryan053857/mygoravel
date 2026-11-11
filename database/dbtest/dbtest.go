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

	"github.com/uptrace/bun"

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
		db = openServer(ctx, t, driver, " WITH (FORCE)")
	case "mysql":
		db = openServer(ctx, t, driver, "")
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

// openServer 在 driver 的测试服务器上创建一个临时数据库并打开它，测试结束时删除。
// 只通过 driver 连接，所以测试只链接应用自己 import 的驱动。
func openServer(ctx context.Context, t testing.TB, driver database.Driver, drop string) *bun.DB {
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
		if _, err := db.ExecContext(cleanCtx, "DROP DATABASE IF EXISTS "+name+drop); err != nil {
			t.Errorf("dbtest: drop database: %v", err)
		}
	})

	db, err := database.Open(ctx, driver, database.Config{DSN: withDatabase(adminDSN, name)})
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

// withDatabase 返回把 dsn 的数据库换成 name 的连接串。dsn 是 URL（postgres://、mysql://），
// 或 PostgreSQL 的 key=value 写法：后出现的 dbname 生效。
func withDatabase(dsn, name string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		u.Path = "/" + name
		// PostgreSQL 的 URL 也可以在查询参数中指定数据库，它优先于路径。
		if q := u.Query(); q.Has("dbname") {
			q.Del("dbname")
			u.RawQuery = q.Encode()
		}
		return u.String()
	}
	return dsn + " dbname=" + name
}

func randomName() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
