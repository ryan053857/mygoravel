package migratecmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database"
)

func TestMakeReadsDriverFromConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	// 只读取 driver，不连接数据库：dsn 指向的目录不存在也没关系
	cfg := writeConfig(t, "database:\n  driver: sqlite\n  dsn: /nonexistent/app.db\n")
	root := newRoot(migratecmd.New(fstest.MapFS{}, dbConfig))
	out, _, err := execute(root, "migrate", "make", "create_articles_table", "--dir", dir, "--config", cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(out)
	if filepath.Dir(path) != dir || !strings.HasSuffix(path, "_create_articles_table.sql") {
		t.Fatalf("out=%q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "TEXT PRIMARY KEY") {
		t.Fatalf("data=%s err=%v", data, err)
	}
}

func TestStandaloneMakeWithoutConfiguration(t *testing.T) {
	dir := t.TempDir()
	out, _, err := execute(newRoot(migratecmd.Make(database.Postgres)), "make", "create_articles_table", "--dir", dir)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(strings.TrimSpace(out))
	if err != nil || !strings.Contains(string(data), "uuid PRIMARY KEY") {
		t.Fatalf("data=%s err=%v", data, err)
	}
}

func TestMakeRequiresExactlyOneName(t *testing.T) {
	for _, args := range [][]string{{"make"}, {"make", "a", "b"}} {
		_, _, err := execute(newRoot(migratecmd.Make(database.Postgres)), args...)
		if command.ExitCode(err) != command.ExitUsage {
			t.Fatalf("args=%v err=%v exit=%d", args, err, command.ExitCode(err))
		}
	}
}
