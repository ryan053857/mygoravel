//go:build integration

package database_test

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/postgres"
)

//go:embed testdata/migrations
var migrationsRoot embed.FS

//go:embed testdata/baseline
var baselineRoot embed.FS

//go:embed testdata/fixture_v1
var fixtureV1Root embed.FS

type item struct {
	ID   uuid.UUID `bun:"id,pk"`
	Name string    `bun:"name"`
	SKU  string    `bun:"sku"`
	Note string    `bun:"note"`
}

func sub(t *testing.T, root embed.FS, dir string) fs.FS {
	t.Helper()
	s, err := migrate.Sub(root, dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestMigrateEmptyAndUpgrade(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, nil)
	ctx := context.Background()
	fixture := sub(t, fixtureV1Root, "testdata/fixture_v1")
	all := sub(t, migrationsRoot, "testdata/migrations")

	p1, err := migrate.NewProvider(db, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, p1); err != nil {
		t.Fatal(err)
	}
	ver, err := p1.GetDBVersion(ctx)
	if err != nil || ver != 1 {
		t.Fatalf("version=%d err=%v", ver, err)
	}

	p2, err := migrate.NewProvider(db, all)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, p2); err != nil {
		t.Fatal(err)
	}
	ver, err = p2.GetDBVersion(ctx)
	if err != nil || ver != 2 {
		t.Fatalf("upgraded version=%d err=%v", ver, err)
	}
}

func TestPublishedBaseline(t *testing.T) {
	live := sub(t, migrationsRoot, "testdata/migrations")
	base := sub(t, baselineRoot, "testdata/baseline")
	if err := migrate.CheckFS(live, base); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentMigrate(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, nil)
	ctx := context.Background()
	all := sub(t, migrationsRoot, "testdata/migrations")
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := migrate.NewProvider(db, all)
			if err != nil {
				errs <- err
				return
			}
			errs <- migrate.Up(ctx, p)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := migrate.NewProvider(db, all)
	if err != nil {
		t.Fatal(err)
	}
	ver, err := p.GetDBVersion(ctx)
	if err != nil || ver != 2 {
		t.Fatalf("version=%d err=%v", ver, err)
	}
}

func TestTxRollbackUniqueAndCancel(t *testing.T) {
	all := sub(t, migrationsRoot, "testdata/migrations")
	db := dbtest.Open(t, postgres.Driver, all)
	ctx := context.Background()

	id := uuid.Must(uuid.NewV7())
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(&item{ID: id, Name: "a", SKU: "s1"}).Exec(ctx)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	err = db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.NewInsert().Model(&item{ID: uuid.Must(uuid.NewV7()), Name: "b", SKU: "s1"}).Exec(ctx)
		if !postgres.IsUniqueViolation(err, "items_sku_key") {
			t.Fatalf("want unique violation, got %v", err)
		}
		return err
	})
	if err == nil {
		t.Fatal("expected tx error")
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
	if err := db.NewSelect().Table("items").Where("sku = ?", "s2").ColumnExpr("count(*)").Scan(ctx, &n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("rolled back row visible: %d", n)
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
}

func TestNonTransactionalMigrationRequiresManualRecovery(t *testing.T) {
	db := dbtest.Open(t, postgres.Driver, nil)
	ctx := context.Background()
	source := fstest.MapFS{"00001_partial.sql": &fstest.MapFile{Data: []byte("-- +goose NO TRANSACTION\n-- +goose Up\nCREATE TABLE partial (id int);\nINSERT INTO missing_table VALUES (1);\n-- +goose Down\nDROP TABLE partial;\n")}}
	p, err := migrate.NewProvider(db, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, p); err == nil {
		t.Fatal("failure not propagated")
	}
	ver, err := p.GetDBVersion(ctx)
	if err != nil || ver != 0 {
		t.Fatal(ver, err)
	}
	var exists bool
	if err := db.QueryRow("SELECT to_regclass('partial') IS NOT NULL").Scan(&exists); err != nil || !exists {
		t.Fatal("partial DDL did not survive", err)
	}
	// 清理已生效的 DDL 并补全依赖表后重试。
	if _, err := db.ExecContext(ctx, "DROP TABLE partial; CREATE TABLE missing_table (id int)"); err != nil {
		t.Fatal(err)
	}
	if err := migrate.Up(ctx, p); err != nil {
		t.Fatal(err)
	}
	ver, err = p.GetDBVersion(ctx)
	if err != nil || ver != 1 {
		t.Fatal(ver, err)
	}
}

func TestDatabaseIsolationWithDSNFormats(t *testing.T) {
	parsed, err := pgx.ParseConfig(dbtest.RequireURL(t, postgres.Driver))
	if err != nil {
		t.Fatal(err)
	}
	quote := func(value string) string {
		return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(value) + "'"
	}
	keyword := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable",
		quote(parsed.Host), parsed.Port, quote(parsed.User), quote(parsed.Password), quote(parsed.Database))
	uri := &url.URL{Scheme: "postgres", Host: fmt.Sprintf("%s:%d", parsed.Host, parsed.Port),
		User: url.UserPassword(parsed.User, parsed.Password), Path: "/" + parsed.Database}
	query := url.Values{"sslmode": {"disable"}, "dbname": {parsed.Database}}
	uri.RawQuery = query.Encode()
	for name, dsn := range map[string]string{"keyword": keyword, "database-query": uri.String()} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(dbtest.EnvURL(postgres.Driver), dsn)
			db := dbtest.Open(t, postgres.Driver, nil)
			var actual string
			if err := db.QueryRow("select current_database()").Scan(&actual); err != nil {
				t.Fatal(err)
			}
			if actual == parsed.Database || !strings.HasPrefix(actual, "test_") {
				t.Fatalf("expected isolated database, got %q", actual)
			}
		})
	}
}
