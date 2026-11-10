package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/database/postgres"
	"github.com/ryan053857/mygoravel/database/sqlite"
)

func TestCreateGuessesTableFromName(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"create_articles_table", "CREATE TABLE articles (\n"},
		{"create_article_tags", "CREATE TABLE article_tags (\n"},
		{"add_published_at_to_articles_table", "ALTER TABLE articles;\n"},
		{"drop_summary_from_articles", "ALTER TABLE articles;\n"},
		{"backfill_slugs", "-- +goose Up\n\n-- +goose Down\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, err := Create(postgres.Driver, t.TempDir(), tc.name)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(path, "_"+tc.name+".sql") {
				t.Fatalf("path=%s", path)
			}
			data, err := os.ReadFile(path)
			if err != nil || !strings.HasPrefix(string(data), "-- +goose Up\n") || !strings.Contains(string(data), tc.want) {
				t.Fatalf("data=%s err=%v", data, err)
			}
		})
	}
	data, _ := os.ReadFile(must(Create(postgres.Driver, t.TempDir(), "create_articles_table")))
	if !strings.Contains(string(data), "DROP TABLE articles;") {
		t.Fatalf("data=%s", data)
	}
}

func TestCreateUsesDriverTypes(t *testing.T) {
	data, _ := os.ReadFile(must(Create(sqlite.Driver, t.TempDir(), "create_articles_table")))
	if !strings.Contains(string(data), "id         TEXT PRIMARY KEY") || strings.Contains(string(data), "DEFAULT") {
		t.Fatalf("data=%s", data)
	}
	if _, err := Create(fakeDriver("oracle"), t.TempDir(), "create_articles_table"); err == nil {
		t.Fatal("unsupported driver accepted")
	}
}

func TestCreateVersionFollowsExistingMigrations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations") // 目录不存在时自动创建
	first, err := Create(postgres.Driver, dir, "create_articles_table")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "29990101000000_future.sql"), []byte(testMigration), 0o600); err != nil {
		t.Fatal(err)
	}
	second, err := Create(postgres.Driver, dir, "add_status_to_articles_table")
	if err != nil || filepath.Base(second) != "29990101000001_add_status_to_articles_table.sql" {
		t.Fatalf("first=%s second=%s err=%v", first, second, err)
	}
}

func TestCreateRejectsNonSnakeCaseNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"", "CreateArticles", "create-articles", "create articles", "_create", "create__articles", "create_articles_", "1_create", "../create"} {
		if _, err := Create(postgres.Driver, dir, name); err == nil {
			t.Errorf("Create(%q) succeeded", name)
		}
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("files written: %v", entries)
	}
}

func must(path string, err error) string {
	if err != nil {
		panic(err)
	}
	return path
}

// fakeDriver 是只有名字的驱动，用于检查不支持的数据库。
type fakeDriver string

func (d fakeDriver) Name() string                  { return string(d) }
func (fakeDriver) Connect(string) (*bun.DB, error) { return nil, errors.New("fake driver") }
