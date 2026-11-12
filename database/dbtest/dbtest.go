// Package dbtest 为测试在 PostgreSQL 测试服务器上创建隔离的临时数据库，测试结束时删除。
// 服务器的管理员连接由环境变量 POSTGRES_TEST_URL 给出。队列用逻辑复制转发任务，
// 服务器需要 wal_level=logical，max_replication_slots 要容得下并行测试的数据库，见 docs/testing.md。
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
		if err := dropSlots(cleanCtx, db, name); err != nil {
			t.Errorf("dbtest: drop replication slots: %v", err)
		}
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

// Replica 返回连接 db 所在测试数据库的只读连接池，代替测试中的只读副本：它和 db 看到同样的数据，没有复制延迟，
// 通过它写入时立即失败（database.ReadOnly），测试能发现误把写操作交给副本的代码。测试结束时关闭。
func Replica(t testing.TB, db *bun.DB) *bun.DB {
	t.Helper()
	ctx := context.Background()
	var name string
	if err := db.NewRaw("SELECT current_database()").Scan(ctx, &name); err != nil {
		t.Fatal(err)
	}
	replica, err := database.Open(ctx, database.Config{DSN: withDatabase(URL(t), name)}, database.Name("replica"), database.ReadOnly())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := replica.Close(); err != nil {
			t.Errorf("dbtest: close replica: %v", err)
		}
	})
	return replica
}

// dropSlots 删除数据库 name 的逻辑复制槽：数据库有槽时 DROP DATABASE 失败，WITH (FORCE) 也不会删除它们。
// 正在使用槽的连接先被结束，它们释放槽之后才能删除，所以重试到全部删除或 ctx 结束。
func dropSlots(ctx context.Context, db *bun.DB, name string) error {
	for {
		if _, err := db.ExecContext(ctx, `SELECT pg_terminate_backend(active_pid) FROM pg_replication_slots
			WHERE database = ? AND active_pid IS NOT NULL`, name); err != nil {
			return err
		}
		var left int
		if err := db.NewRaw(`SELECT count(*) FROM (SELECT pg_drop_replication_slot(slot_name) FROM pg_replication_slots
			WHERE database = ? AND NOT active) dropped`, name).Scan(ctx, &left); err != nil {
			return err
		}
		if err := db.NewRaw("SELECT count(*) FROM pg_replication_slots WHERE database = ?", name).Scan(ctx, &left); err != nil {
			return err
		}
		if left == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-time.After(50 * time.Millisecond):
		}
	}
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
