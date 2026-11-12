package migrate_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// 建表骨架和空骨架不经修改即可执行。
func TestCreatedStubsRun(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"create_articles_table", "backfill_slugs"} {
		if _, err := migrate.Create(dir, name); err != nil {
			t.Fatal(err)
		}
	}
	db := dbtest.Open(t, os.DirFS(dir))
	now := time.Now()
	if _, err := db.ExecContext(context.Background(), "INSERT INTO articles (id, created_at, updated_at) VALUES (?, ?, ?)",
		uuid.Must(uuid.NewV7()), now, now); err != nil {
		t.Fatal(err)
	}
}
