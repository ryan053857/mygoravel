package migrate_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/sqlite"
)

func TestCreatedStubsRunOnSQLite(t *testing.T) {
	testStubsRun(t, sqlite.Driver)
}

// testStubsRun 检查建表骨架和空骨架不经修改即可执行。
func testStubsRun(t *testing.T, driver database.Driver) {
	dir := t.TempDir()
	for _, name := range []string{"create_articles_table", "backfill_slugs"} {
		if _, err := migrate.Create(driver, dir, name); err != nil {
			t.Fatal(err)
		}
	}
	db := dbtest.Open(t, driver, os.DirFS(dir))
	now := time.Now()
	if _, err := db.ExecContext(context.Background(), "INSERT INTO articles (id, created_at, updated_at) VALUES (?, ?, ?)",
		uuid.Must(uuid.NewV7()), now, now); err != nil {
		t.Fatal(err)
	}
}
