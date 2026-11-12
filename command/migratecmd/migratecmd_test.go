package migratecmd_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/migratecmd"
	"github.com/ryan053857/mygoravel/database"
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
