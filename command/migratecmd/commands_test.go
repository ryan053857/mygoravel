package migratecmd_test

import (
	"context"
	"net/url"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database/dbtest"
)

// TestMigrateCommands 在一个空数据库上依次检查 status、up、down 的输出。
func TestMigrateCommands(t *testing.T) {
	path := serverConfig(t)
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

// serverConfig 创建临时数据库，返回指向它的配置文件路径。
func serverConfig(t *testing.T) string {
	db := dbtest.Open(t, nil)
	var name string
	if err := db.QueryRowContext(context.Background(), "SELECT current_database()").Scan(&name); err != nil {
		t.Fatal(err)
	}
	dsn, err := url.Parse(dbtest.URL(t))
	if err != nil {
		t.Fatal(err)
	}
	dsn.Path = "/" + name
	return writeConfig(t, "database:\n  dsn: "+dsn.String()+"\n")
}
