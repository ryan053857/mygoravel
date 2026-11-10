package migratecmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
)

func TestMakeWithoutConfigurationOrDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "migrations")
	root := newRoot(migratecmd.New(fstest.MapFS{}, dbConfig))
	out, _, err := execute(root, "migrate", "make", "create_articles_table", "--dir", dir, "--config", filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	path := strings.TrimSpace(out)
	if filepath.Dir(path) != dir || !strings.HasSuffix(path, "_create_articles_table.sql") {
		t.Fatalf("out=%q", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
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
