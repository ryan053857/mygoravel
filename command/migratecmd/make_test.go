package migratecmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/command/migratecmd"
)

func TestMakeWithoutConfiguration(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	root := newRoot(migratecmd.New(fstest.MapFS{}, dbConfig))
	out, _, err := execute(root, "migrate", "make", "create_articles_table", "--dir", dir, "--config", filepath.Join(dir, "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(out)
	if filepath.Dir(path) != dir || !strings.HasSuffix(path, "_create_articles_table.sql") {
		t.Fatalf("out=%q", out)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "uuid PRIMARY KEY") {
		t.Fatalf("data=%s err=%v", data, err)
	}
}

func TestStandaloneMakeWithoutConfiguration(t *testing.T) {
	dir := t.TempDir()
	out, _, err := execute(newRoot(migratecmd.Make()), "make", "create_articles_table", "--dir", dir)
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
		_, _, err := execute(newRoot(migratecmd.Make()), args...)
		if command.ExitCode(err) != command.ExitUsage {
			t.Fatalf("args=%v err=%v exit=%d", args, err, command.ExitCode(err))
		}
	}
}
