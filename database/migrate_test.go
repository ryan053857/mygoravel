package database_test

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/database/migrate"
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

// itemMigrations 是两个测试迁移：建表（sku 唯一）和加列。
var itemMigrations = [2]string{
	"CREATE TABLE items (id uuid PRIMARY KEY, name text NOT NULL, sku text NOT NULL, CONSTRAINT items_sku_key UNIQUE (sku));",
	"ALTER TABLE items ADD COLUMN note text NOT NULL DEFAULT '';",
}

// items 返回前 n 个测试迁移。
func items(n int) fs.FS {
	m := itemMigrations
	files := fstest.MapFS{"00001_init.sql": {Data: []byte("-- +goose Up\n" + m[0] + "\n-- +goose Down\nDROP TABLE items;\n")}}
	if n > 1 {
		files["00002_add_notes.sql"] = &fstest.MapFile{Data: []byte("-- +goose Up\n" + m[1] + "\n-- +goose Down\nALTER TABLE items DROP COLUMN note;\n")}
	}
	return files
}

func TestMigrateEmptyAndUpgrade(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, nil)
	ctx := context.Background()
	for want, migrations := range []fs.FS{items(1), items(2)} {
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
}

// 多个实例同时迁移时由数据库锁串行执行。
func TestConcurrentMigrate(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, nil)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range cap(errs) {
		wg.Go(func() {
			p, err := migrate.NewProvider(db, items(2))
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
	p, err := migrate.NewProvider(db, items(2))
	if err != nil {
		t.Fatal(err)
	}
	if ver, err := p.GetDBVersion(ctx); err != nil || ver != 2 {
		t.Fatalf("version=%d err=%v", ver, err)
	}
}

func TestTxRollbackUniqueAndCancel(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, nil)
	ctx := context.Background()
	p, err := migrate.NewProvider(db, items(2))
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
	if !database.IsUniqueViolation(err, "items_sku_key") {
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

	// 事先取消的 ctx：超时的 ctx 要等计时器关闭 Done，机器繁忙时事务可能照常执行。
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	err = db.RunInTx(cctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(&item{ID: uuid.Must(uuid.NewV7()), Name: "d", SKU: "s3"}).Exec(ctx)
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancel error, got %v", err)
	}
}

// 迁移失败时回滚整个迁移，已执行的建表不会留下。
func TestFailedMigrationRollsBack(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, nil)
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
	if _, err := db.ExecContext(ctx, "SELECT COUNT(*) FROM partial"); err == nil {
		t.Fatal("partial table kept")
	}
	// 补全依赖表后重试。
	if _, err := db.ExecContext(ctx, "CREATE TABLE missing_table (id INT)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, p); err != nil {
		t.Fatal(err)
	}
	if ver, err := p.GetDBVersion(ctx); err != nil || ver != 1 {
		t.Fatalf("version=%d err=%v", ver, err)
	}
}

func TestPublishedBaseline(t *testing.T) {
	t.Parallel()
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
