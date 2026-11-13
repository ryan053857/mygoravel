package migratecmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/command/migratecmd"
	"github.com/go-tenon/tenon/database/migrate"
)

func TestPublishWithoutConfigurationOrDatabase(t *testing.T) {
	source := migrate.Source{Name: "files", FS: fstest.MapFS{
		"0001_create_files.sql": {Data: []byte("-- +goose Up\nCREATE TABLE files (id integer);\n-- +goose Down\nDROP TABLE files;\n")},
	}}
	dir := filepath.Join(t.TempDir(), "migrations")
	for i := range 2 {
		migrationCmd := migratecmd.New(fstest.MapFS{}, dbConfig)
		migrationCmd.AddCommand(migratecmd.Publish(source))
		root := newRoot(migrationCmd)
		out, _, err := execute(root, "migrate", "publish", "--dir", dir, "--config", filepath.Join(t.TempDir(), "absent.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			name := strings.TrimSpace(out)
			if !strings.HasSuffix(name, "_files_0001_create_files.sql") {
				t.Fatalf("out=%q", out)
			}
			if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		} else if out != "" {
			t.Fatalf("repeat out=%q", out)
		}
	}
}

func TestPublishRejectsExtraArguments(t *testing.T) {
	root := newRoot(migratecmd.Publish())
	_, _, err := execute(root, "publish", "extra")
	if command.ExitCode(err) != command.ExitUsage {
		t.Fatalf("err=%v exit=%d", err, command.ExitCode(err))
	}
}
