package dbtest_test

import (
	"context"
	"testing"

	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/database/dbtest"
)

// 测试结束时，数据库连同它的逻辑复制槽一起删除。
func TestDropsReplicationSlots(t *testing.T) {
	ctx := context.Background()
	var name string
	t.Run("open", func(t *testing.T) {
		db := dbtest.Open(t, nil)
		if err := db.NewRaw("SELECT current_database()").Scan(ctx, &name); err != nil {
			t.Fatal(err)
		}
		for _, slot := range []string{name + "_a", name + "_b"} {
			if _, err := db.ExecContext(ctx, "SELECT pg_create_logical_replication_slot(?, 'pgoutput')", slot); err != nil {
				t.Fatal(err)
			}
		}
	})

	admin, err := database.Open(ctx, database.Config{DSN: dbtest.URL(t)})
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var slots, dbs int
	if err := admin.NewRaw("SELECT count(*) FROM pg_replication_slots WHERE database = ?", name).Scan(ctx, &slots); err != nil {
		t.Fatal(err)
	}
	if err := admin.NewRaw("SELECT count(*) FROM pg_database WHERE datname = ?", name).Scan(ctx, &dbs); err != nil {
		t.Fatal(err)
	}
	if slots != 0 || dbs != 0 {
		t.Fatalf("left %d slots, %d databases", slots, dbs)
	}
}
