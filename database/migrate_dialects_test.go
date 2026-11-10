package database_test

import (
	"context"
	"database/sql"
	"embed"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
	"github.com/ryan053857/mygoravel/database/sqlite"
	"github.com/ryan053857/mygoravel/internal/dbtesting"
)

//go:embed testdata/migrations
var migrationsRoot embed.FS

//go:embed testdata/baseline
var baselineRoot embed.FS

type item struct {
	ID   uuid.UUID `bun:"id,pk"`
	Name string    `bun:"name"`
	SKU  string    `bun:"sku"`
	Note string    `bun:"note"`
}

// itemMigrations 是各数据库的两个测试迁移：建表（sku 唯一）和加列。
var itemMigrations = map[string][2]string{
	"postgres": {
		"CREATE TABLE items (id uuid PRIMARY KEY, name text NOT NULL, sku text NOT NULL, CONSTRAINT items_sku_key UNIQUE (sku));",
		"ALTER TABLE items ADD COLUMN note text NOT NULL DEFAULT '';",
	},
	"sqlite": {
		"CREATE TABLE items (id TEXT PRIMARY KEY, name TEXT NOT NULL, sku TEXT NOT NULL, CONSTRAINT items_sku_key UNIQUE (sku));",
		"ALTER TABLE items ADD COLUMN note TEXT NOT NULL DEFAULT '';",
	},
	"mysql": {
		"CREATE TABLE items (id CHAR(36) PRIMARY KEY, name VARCHAR(100) NOT NULL, sku VARCHAR(100) NOT NULL, CONSTRAINT items_sku_key UNIQUE (sku));",
		"ALTER TABLE items ADD COLUMN note VARCHAR(255) NOT NULL DEFAULT '';",
	},
}

// items 返回 driver 的前 n 个测试迁移。
func items(driver database.Driver, n int) fs.FS {
	m := itemMigrations[driver.Name()]
	files := fstest.MapFS{"00001_init.sql": {Data: []byte("-- +goose Up\n" + m[0] + "\n-- +goose Down\nDROP TABLE items;\n")}}
	if n > 1 {
		files["00002_add_notes.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\n" + m[1] + "\n-- +goose Down\nALTER TABLE items DROP COLUMN note;\n")}
	}
	return files
}

// isSKUTaken 用 driver 自己的判断函数识别 items_sku_key 冲突；SQLite 按列匹配。
func isSKUTaken(driver database.Driver, err error) bool {
	switch driver.Name() {
	case "postgres":
		return postgres.IsUniqueViolation(err, "items_sku_key")
	case "mysql":
		return mysql.IsUniqueViolation(err, "items_sku_key")
	default:
		return sqlite.IsUniqueViolation(err, "items.sku")
	}
}

func TestMigrateEmptyAndUpgrade(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, nil)
		ctx := context.Background()
		for want, migrations := range []fs.FS{items(driver, 1), items(driver, 2)} {
			p, err := migrate.NewProvider(db, migrations)
			if err != nil {
				t.Fatal(err)
			}
			if err := migrate.Up(ctx, p); err != nil {
				t.Fatal(err)
			}
			if ver, err := p.GetDBVersion(ctx); err != nil || ver != int64(want+1) {
				t.Fatalf("version=%d err=%v", ver, err)
			}
		}
	})
}

// 多个实例同时迁移时由数据库锁串行执行。SQLite 只有一个数据库文件，迁移不加锁。
func TestConcurrentMigrate(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		if driver.Name() == "sqlite" {
			t.Skip("SQLite 的迁移不加锁，部署时不应并发执行")
		}
		db := dbtesting.Open(t, driver, nil)
		ctx := context.Background()
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range cap(errs) {
			wg.Go(func() {
				p, err := migrate.NewProvider(db, items(driver, 2))
				if err != nil {
					errs <- err
					return
				}
				errs <- migrate.Up(ctx, p)
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
		p, err := migrate.NewProvider(db, items(driver, 2))
		if err != nil {
			t.Fatal(err)
		}
		if ver, err := p.GetDBVersion(ctx); err != nil || ver != 2 {
			t.Fatalf("version=%d err=%v", ver, err)
		}
	})
}

func TestTxRollbackUniqueAndCancel(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, nil)
		ctx := context.Background()
		p, err := migrate.NewProvider(db, items(driver, 2))
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Up(ctx, p); err != nil {
			t.Fatal(err)
		}

		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			_, err := tx.NewInsert().Model(&item{ID: uuid.Must(uuid.NewV7()), Name: "a", SKU: "s1"}).Exec(ctx)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			_, err := tx.NewInsert().Model(&item{ID: uuid.Must(uuid.NewV7()), Name: "b", SKU: "s1"}).Exec(ctx)
			return err
		})
		if !isSKUTaken(driver, err) {
			t.Fatalf("want unique violation, got %v", err)
		}

		err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
			if _, err := tx.NewInsert().Model(&item{ID: uuid.Must(uuid.NewV7()), Name: "c", SKU: "s2"}).Exec(ctx); err != nil {
				return err
			}
			return context.Canceled
		})
		if err == nil {
			t.Fatal("expected rollback")
		}
		var n int
		if err := db.NewSelect().Table("items").Where("sku = ?", "s2").ColumnExpr("count(*)").Scan(ctx, &n); err != nil || n != 0 {
			t.Fatalf("rolled back row visible: n=%d err=%v", n, err)
		}

		cctx, cancel := context.WithTimeout(ctx, time.Millisecond)
		defer cancel()
		time.Sleep(2 * time.Millisecond)
		err = db.RunInTx(cctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
			_, err := tx.NewInsert().Model(&item{ID: uuid.Must(uuid.NewV7()), Name: "d", SKU: "s3"}).Exec(ctx)
			return err
		})
		if err == nil {
			t.Fatal("expected cancel error")
		}
	})
}

// 迁移失败时，PostgreSQL 和 SQLite 回滚整个迁移；MySQL 的 DDL 会隐式提交，已执行的建表保留下来，
// 版本号却没有更新，需要人工清理后重试。
func TestFailedMigrationKeepsDDLOnlyOnMySQL(t *testing.T) {
	dbtesting.ForEach(t, func(t *testing.T, driver database.Driver) {
		db := dbtesting.Open(t, driver, nil)
		ctx := context.Background()
		source := fstest.MapFS{"00001_partial.sql": {Data: []byte("-- +goose Up\nCREATE TABLE partial (id INT);\nINSERT INTO missing_table VALUES (1);\n-- +goose Down\nDROP TABLE partial;\n")}}
		p, err := migrate.NewProvider(db, source)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrate.Up(ctx, p); err == nil {
			t.Fatal("failure not propagated")
		}
		if ver, err := p.GetDBVersion(ctx); err != nil || ver != 0 {
			t.Fatalf("version=%d err=%v", ver, err)
		}
		_, err = db.ExecContext(ctx, "SELECT COUNT(*) FROM partial")
		if kept := err == nil; kept != (driver.Name() == "mysql") {
			t.Fatalf("partial table kept=%v on %s", kept, driver.Name())
		}
		// 清理已生效的 DDL、补全依赖表后重试。
		for _, q := range []string{"DROP TABLE IF EXISTS partial", "CREATE TABLE missing_table (id INT)"} {
			if _, err := db.ExecContext(ctx, q); err != nil {
				t.Fatal(err)
			}
		}
		if err := migrate.Up(ctx, p); err != nil {
			t.Fatal(err)
		}
		if ver, err := p.GetDBVersion(ctx); err != nil || ver != 1 {
			t.Fatalf("version=%d err=%v", ver, err)
		}
	})
}

func TestPublishedBaseline(t *testing.T) {
	live, err := migrate.Sub(migrationsRoot, "testdata/migrations")
	if err != nil {
		t.Fatal(err)
	}
	base, err := migrate.Sub(baselineRoot, "testdata/baseline")
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.CheckFS(live, base); err != nil {
		t.Fatal(err)
	}
}
