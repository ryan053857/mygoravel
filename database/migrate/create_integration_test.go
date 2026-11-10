//go:build integration

package migrate_test

import (
	"context"
	"os"
	"testing"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// 生成的建表骨架和空骨架不经修改即可执行。
func TestCreatedStubsRun(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"create_articles_table", "backfill_slugs"} {
		if _, err := migrate.Create(dir, name); err != nil {
			t.Fatal(err)
		}
	}
	db := dbtest.Open(t, os.DirFS(dir))
	if _, err := db.ExecContext(context.Background(), "INSERT INTO articles DEFAULT VALUES"); err != nil {
		t.Fatal(err)
	}
}
