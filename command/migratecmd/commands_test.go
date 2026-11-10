package migratecmd_test

import (
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/command/migratecmd"
)

func TestMigrateCommandsOnSQLite(t *testing.T) {
	path := writeConfig(t, "database:\n  driver: sqlite\n  dsn: "+filepath.Join(t.TempDir(), "app.db")+"\n")
	testMigrateCommands(t, path)
}

// testMigrateCommands 用 configPath 指向的空数据库依次检查 status、up、down 的输出。
func testMigrateCommands(t *testing.T, configPath string) {
	path := configPath
	migrations := fstest.MapFS{
		"00001_create_notes.sql": {Data: []byte("-- +goose Up\nCREATE TABLE notes (id int);\n-- +goose Down\nDROP TABLE notes;\n")},
		"00002_add_body.sql":     {Data: []byte("-- +goose Up\nALTER TABLE notes ADD body text;\n-- +goose Down\nALTER TABLE notes DROP body;\n")},
	}
	run := func(args ...string) string {
		t.Helper()
		root := newRoot(migratecmd.New(migrations, dbConfig))
		out, _, err := execute(root, append(args, "--config", path)...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	// status 每行形如 "1 applied 2026-11-10 09:00:00 00001_create_notes.sql"，只比较版本、状态和文件名
	status := func() string {
		var rows []string
		for _, line := range strings.Split(strings.TrimSpace(run("migrate", "status")), "\n")[1:] {
			f := strings.Fields(line)
			rows = append(rows, f[0]+" "+f[1]+" "+f[len(f)-1])
		}
		return strings.Join(rows, "\n")
	}

	if got := status(); got != "1 pending 00001_create_notes.sql\n2 pending 00002_add_body.sql" {
		t.Fatalf("status before up:\n%s", got)
	}
	if out := run("migrate", "up"); !strings.Contains(out, "up  00001_create_notes.sql") || !strings.Contains(out, "up  00002_add_body.sql") {
		t.Fatalf("up output:\n%s", out)
	}
	if got := status(); got != "1 applied 00001_create_notes.sql\n2 applied 00002_add_body.sql" {
		t.Fatalf("status after up:\n%s", got)
	}
	if out := run("migrate", "down"); !strings.Contains(out, "down  00002_add_body.sql") {
		t.Fatalf("down output:\n%s", out)
	}
	if got := status(); got != "1 applied 00001_create_notes.sql\n2 pending 00002_add_body.sql" {
		t.Fatalf("status after down:\n%s", got)
	}
}
