package activity_test

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/activity"
	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/mysql"
	"github.com/ryan053857/mygoravel/database/postgres"
	"github.com/ryan053857/mygoravel/database/sqlite"
)

// 此契约也由外部消费模块运行，保证下载模块携带的内嵌 SQL 能通过公开命令发布。
func TestEmbeddedMigrationsPublishThroughPublicCommand(t *testing.T) {
	for _, driver := range []database.Driver{postgres.Driver, mysql.Driver, sqlite.Driver} {
		t.Run(driver.Name(), func(t *testing.T) {
			source, err := fs.Sub(activity.Migrations(), driver.Name())
			if err != nil {
				t.Fatal(err)
			}
			entries, err := fs.ReadDir(source, ".")
			if err != nil || len(entries) == 0 {
				t.Fatalf("embedded migrations=%v err=%v", entries, err)
			}
			dir := filepath.Join(t.TempDir(), "migrations")
			run := func() []string {
				t.Helper()
				root := command.Root("api", "test")
				root.AddCommand(migratecmd.Publish(driver, migrate.Source{Name: "activity", FS: activity.Migrations()}))
				root.SetArgs([]string{"publish", "--dir", dir, "--config", filepath.Join(dir, "absent.yaml")})
				var output bytes.Buffer
				root.SetOut(&output)
				if err := root.Execute(); err != nil {
					t.Fatal(err)
				}
				return strings.Fields(output.String())
			}
			names := run()
			if len(names) != len(entries) {
				t.Fatalf("published=%v embedded=%v", names, entries)
			}
			for _, entry := range entries {
				original, err := fs.ReadFile(source, entry.Name())
				if err != nil {
					t.Fatal(err)
				}
				var destination string
				for _, name := range names {
					if strings.HasSuffix(name, "_activity_"+entry.Name()) {
						destination = name
						break
					}
				}
				if destination == "" {
					t.Fatalf("missing published %s in %v", entry.Name(), names)
				}
				published, err := os.ReadFile(filepath.Join(dir, destination))
				if err != nil {
					t.Fatal(err)
				}
				want := append([]byte("-- mygoravel: activity/"+strings.TrimSuffix(entry.Name(), ".sql")+"\n"), original...)
				if !bytes.Equal(published, want) {
					t.Fatalf("published content differs from embedded SQL: %s", destination)
				}
			}
			if again := run(); len(again) != 0 {
				t.Fatalf("repeat published=%v", again)
			}
		})
	}
}
