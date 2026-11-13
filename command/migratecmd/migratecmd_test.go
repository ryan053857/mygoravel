package migratecmd_test

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/command/migratecmd"
	"github.com/go-tenon/tenon/database"
	"github.com/go-tenon/tenon/database/dbtest"
)

type settings struct {
	Database database.Config `yaml:"database"`
}

func dbConfig(s settings) database.Config { return s.Database }

func newRoot(cmds ...*cobra.Command) *cobra.Command {
	root := command.Root("api", "test")
	root.AddCommand(cmds...)
	return root
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func execute(root *cobra.Command, args ...string) (stdout, stderr string, err error) {
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.ExecuteContext(context.Background())
	return out.String(), errOut.String(), err
}

func TestExitCodes(t *testing.T) {
	// 端口 1 上没有 PostgreSQL，连接会立即被拒绝
	unreachable := writeConfig(t, "database:\n  dsn: postgres://postgres@127.0.0.1:1/blog?sslmode=disable&connect_timeout=2\n")
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"migrate", "status", "--config", unreachable}, command.ExitUnavailable},
		{[]string{"migrate", "up", "--config", missing}, command.ExitConfig},
		{[]string{"migrate", "up", "extra"}, command.ExitUsage},
		{[]string{"migrate", "status", "--unknown"}, command.ExitUsage},
	} {
		root := newRoot(migratecmd.New(fstest.MapFS{}, dbConfig))
		if _, _, err := execute(root, tc.args...); command.ExitCode(err) != tc.want {
			t.Errorf("args=%v exit=%d want=%d err=%v", tc.args, command.ExitCode(err), tc.want, err)
		}
	}
}

func TestHelpWithoutSubcommand(t *testing.T) {
	out, _, err := execute(newRoot(migratecmd.New(fstest.MapFS{}, dbConfig)), "migrate")
	if err != nil || !bytes.Contains([]byte(out), []byte("status")) {
		t.Fatalf("out=%q err=%v", out, err)
	}
}

// 另一个数据库的迁移命令挂在 migrate 下，迁移各自的数据库，互不影响。
func TestNamedDatabase(t *testing.T) {
	type twoDBs struct {
		Database  database.Config `yaml:"database"`
		Analytics database.Config `yaml:"analytics"`
	}
	main := dbtest.Open(t, nil)
	analytics := dbtest.Open(t, nil)
	dsn := func(db *bun.DB) string {
		var name string
		if err := db.NewRaw("SELECT current_database()").Scan(context.Background(), &name); err != nil {
			t.Fatal(err)
		}
		u, err := url.Parse(dbtest.URL(t))
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/" + name
		return u.String()
	}
	cfg := writeConfig(t, "database:\n  dsn: "+dsn(main)+"\nanalytics:\n  dsn: "+dsn(analytics)+"\n")
	mainFS := fstest.MapFS{"00001_notes.sql": {Data: []byte("-- +goose Up\nCREATE TABLE notes (id int);\n-- +goose Down\nDROP TABLE notes;\n")}}
	analyticsFS := fstest.MapFS{"00001_events.sql": {Data: []byte("-- +goose Up\nCREATE TABLE events (id int);\n-- +goose Down\nDROP TABLE events;\n")}}
	newCmds := func() *cobra.Command {
		mig := migratecmd.New(mainFS, func(c twoDBs) database.Config { return c.Database })
		mig.AddCommand(migratecmd.New(analyticsFS, func(c twoDBs) database.Config { return c.Analytics }, migratecmd.Name("analytics")))
		return newRoot(mig)
	}

	if _, _, err := execute(newCmds(), "migrate", "analytics", "up", "--config", cfg); err != nil {
		t.Fatal(err)
	}
	tables := func(db *bun.DB) []string {
		var names []string
		if err := db.NewRaw("SELECT tablename FROM pg_tables WHERE schemaname = 'public' AND tablename IN ('notes', 'events') ORDER BY 1").Scan(context.Background(), &names); err != nil {
			t.Fatal(err)
		}
		return names
	}
	if got := tables(analytics); len(got) != 1 || got[0] != "events" {
		t.Fatalf("analytics tables %v", got)
	}
	if got := tables(main); len(got) != 0 {
		t.Fatalf("main migrated by the analytics command: %v", got)
	}
	if _, _, err := execute(newCmds(), "migrate", "up", "--config", cfg); err != nil {
		t.Fatal(err)
	}
	if got := tables(main); len(got) != 1 || got[0] != "notes" {
		t.Fatalf("main tables %v", got)
	}

	// make 默认写入 migrations/analytics。
	dir := t.TempDir()
	t.Chdir(dir)
	out, _, err := execute(newCmds(), "migrate", "analytics", "make", "add_index")
	if err != nil || !strings.HasPrefix(strings.TrimSpace(out), filepath.Join("migrations", "analytics")) {
		t.Fatalf("make: %q %v", out, err)
	}
}
