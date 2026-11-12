// Package dbtest 为测试在 PostgreSQL 测试服务器上创建隔离的临时数据库，测试结束时删除。
// 服务器的管理员连接由环境变量 POSTGRES_TEST_URL 给出。
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io/fs"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// EnvURL 是测试服务器管理员连接所在的环境变量。
const EnvURL = "POSTGRES_TEST_URL"

// URL 返回测试服务器的管理员连接，环境变量未设置时使测试失败。
func URL(t testing.TB) string {
	t.Helper()
	url := os.Getenv(EnvURL)
	if url == "" {
		t.Fatalf("%s is required for this test", EnvURL)
	}
	return url
}

// Open 为当前测试创建一个空的临时数据库，migrations 非 nil 时执行迁移，返回 *bun.DB；测试结束时删除。
func Open(t testing.TB, migrations fs.FS) *bun.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminDSN := URL(t)
	admin, err := database.Open(ctx, database.Config{DSN: adminDSN})
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
		db, err := database.Open(cleanCtx, database.Config{DSN: adminDSN})
		if err != nil {
			t.Errorf("dbtest: reopen admin: %v", err)
			return
		}
		defer db.Close()
		if _, err := db.ExecContext(cleanCtx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("dbtest: drop database: %v", err)
		}
	})

	db, err := database.Open(ctx, database.Config{DSN: withDatabase(adminDSN, name)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("dbtest: close database: %v", err)
		}
	})

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

// withDatabase 返回把 dsn 的数据库换成 name 的连接串。dsn 是 postgres:// URL，
// 或 key=value 写法：后出现的 dbname 生效。
func withDatabase(dsn, name string) string {
	if u, err := url.Parse(dsn); err == nil && u.Scheme != "" && u.Host != "" {
		u.Path = "/" + name
		// URL 也可以在查询参数中指定数据库，它优先于路径。
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
