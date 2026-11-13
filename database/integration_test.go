package database_test

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5"

	"github.com/go-tenon/tenon/database/dbtest"
	"github.com/go-tenon/tenon/database/migrate"
)

func TestNonTransactionalMigrationRequiresManualRecovery(t *testing.T) {
	t.Parallel()
	db := dbtest.Open(t, nil)
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
	parsed, err := pgx.ParseConfig(dbtest.URL(t))
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
			t.Setenv(dbtest.EnvURL, dsn)
			db := dbtest.Open(t, nil)
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
