package migratecmd_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database/migrate"
	"github.com/ryan053857/mygoravel/database/postgres"
)

func TestPublishWithoutConfigurationOrDatabase(t *testing.T) {
	source := migrate.Source{Name: "files", FS: fstest.MapFS{
		"postgres/0001_create_files.sql": {Data: []byte("-- +goose Up\nCREATE TABLE files (id integer);\n-- +goose Down\nDROP TABLE files;\n")},
	}}
	dir := filepath.Join(t.TempDir(), "migrations")
	for i := range 2 {
		migrationCmd := migratecmd.New(fstest.MapFS{}, postgres.Driver, dbConfig)
		migrationCmd.AddCommand(migratecmd.Publish(postgres.Driver, source))
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
	root := newRoot(migratecmd.Publish(postgres.Driver))
	_, _, err := execute(root, "publish", "extra")
	if command.ExitCode(err) != command.ExitUsage {
		t.Fatalf("err=%v exit=%d", err, command.ExitCode(err))
	}
}
