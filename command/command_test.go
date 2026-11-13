package command_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/errorx"
)

type settings struct {
	Name string `yaml:"name"`
}

func (s settings) Validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is required")
	}
	return nil
}

// writeConfig 在临时目录中写入配置文件并返回路径。
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

const minimalConfig = "name: blog\n"

// newRoot 返回装配了 cmds 的根命令。
func newRoot(cmds ...*cobra.Command) *cobra.Command {
	root := command.Root("api", "test")
	root.AddCommand(cmds...)
	return root
}

func TestRootHasNoCommandsByDefault(t *testing.T) {
	t.Parallel()
	root := command.Root("api", "test")
	if len(root.Commands()) != 0 {
		t.Fatalf("commands = %v", root.Commands())
	}
	out, _, err := execute(root)
	if err != nil || !strings.Contains(out, "--config") {
		t.Fatalf("help out=%q err=%v", out, err)
	}
}

func TestRunReceivesConfigAndContext(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, minimalConfig)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got settings
	root := newRoot(command.Run("work", "worker", func(runCtx context.Context, cfg settings) error {
		if runCtx != ctx {
			t.Fatal("run did not receive the command context")
		}
		got = cfg
		return nil
	}))
	root.SetArgs([]string{"work", "--config", path})
	if err := root.ExecuteContext(ctx); err != nil {
		t.Fatal(err)
	}
	if got.Name != "blog" {
		t.Fatalf("cfg = %+v", got)
	}
}

func TestConfigErrorsExitWithConfigCode(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	invalid := writeConfig(t, "name: ''\n")
	for _, args := range [][]string{
		{"config", "check", "--config", missing},
		{"config", "check", "--config", invalid},
		{"config", "show", "--config", missing},
		{"serve", "--config", missing},
	} {
		root := newRoot(
			command.Config[settings](),
			command.Run("serve", "serve", func(context.Context, settings) error {
				t.Fatal("serve ran with invalid config")
				return nil
			}),
		)
		if _, _, err := execute(root, args...); command.ExitCode(err) != command.ExitConfig {
			t.Errorf("args=%v exit=%d err=%v", args, command.ExitCode(err), err)
		}
	}
}

func TestConfigCheckAndShow(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, minimalConfig)
	root := func() *cobra.Command { return newRoot(command.Config[settings]()) }

	if out, _, err := execute(root(), "config", "check", "--config", path); err != nil || out != "" {
		t.Fatalf("check out=%q err=%v", out, err)
	}
	out, _, err := execute(root(), "config", "show", "--config", path)
	if err != nil || !strings.Contains(out, "name: blog\n") || strings.Contains(out, "Name") {
		t.Fatalf("yaml out=%q err=%v", out, err)
	}
	out, _, err = execute(root(), "config", "show", "--format", "json", "--config", path)
	if err != nil || !strings.Contains(out, `"Name": "blog"`) {
		t.Fatalf("json out=%q err=%v", out, err)
	}
	if _, _, err := execute(root(), "config", "show", "--format", "toml", "--config", path); command.ExitCode(err) != command.ExitUsage {
		t.Fatalf("invalid format exit=%d err=%v", command.ExitCode(err), err)
	}
}

func TestLoadConfigInCustomCommand(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, minimalConfig)
	root := newRoot(&cobra.Command{Use: "hello", RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := command.LoadConfig[settings](cmd)
		if err != nil {
			return err
		}
		cmd.Println("hello", cfg.Name)
		return nil
	}})
	out, _, err := execute(root, "hello", "--config", path)
	if err != nil || out != "hello blog\n" {
		t.Fatalf("out=%q err=%v", out, err)
	}

	orphan := &cobra.Command{Use: "orphan"}
	if _, err := command.LoadConfig[settings](orphan); err == nil {
		t.Fatal("LoadConfig without --config flag succeeded")
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"unknown"},
		{"--unknown"},
		{"serve", "extra"},
		{"serve", "--unknown"},
		{"config", "extra"},
		{"config", "check", "extra"},
		{"hello", "extra"},
	} {
		root := newRoot(
			command.Run("serve", "serve", func(context.Context, settings) error {
				t.Fatal("invalid arguments ran serve")
				return nil
			}),
			command.Config[settings](),
			&cobra.Command{Use: "hello", Args: command.NoArgs, RunE: func(*cobra.Command, []string) error {
				t.Fatal("invalid arguments ran hello")
				return nil
			}},
		)
		if _, _, err := execute(root, args...); command.ExitCode(err) != command.ExitUsage {
			t.Errorf("args=%v exit=%d err=%v", args, command.ExitCode(err), err)
		}
	}
}

func TestExitCode(t *testing.T) {
	t.Parallel()
	unavailable := errorx.Define(errorx.Unavailable, "demo.db")
	for _, tc := range []struct {
		err  error
		want int
	}{
		{nil, 0},
		{command.AsExitError(command.ExitConfig, unavailable.New()), command.ExitConfig},
		{fmt.Errorf("sync: %w", unavailable.Wrap(context.DeadlineExceeded)), command.ExitUnavailable},
		{errorx.Define(errorx.NotFound, "demo.missing").New(), command.ExitFailure},
		{errorx.Define(errorx.Internal, "demo.bug").New(), command.ExitInternal},
		{context.Canceled, command.ExitInternal},
	} {
		if got := command.ExitCode(tc.err); got != tc.want {
			t.Fatalf("ExitCode(%v) = %d, want %d", tc.err, got, tc.want)
		}
	}
	if command.AsExitError(command.ExitConfig, nil) != nil {
		t.Fatal("AsExitError(nil) must be nil")
	}
}

// projectDir 创建一个临时项目目录并切换进去，files 的路径相对于该目录。
func projectDir(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
}

// loadedName 用 --config 等参数加载配置，返回其中的 name。
func loadedName(t *testing.T, args ...string) string {
	t.Helper()
	var name string
	root := newRoot(command.Run("name", "print name", func(_ context.Context, cfg settings) error {
		name = cfg.Name
		return nil
	}))
	if _, _, err := execute(root, append([]string{"name"}, args...)...); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return name
}

func TestConfigFiles(t *testing.T) {
	projectDir(t, map[string]string{
		"config/app.yaml":   "name: app\n",
		"config/prod.yaml":  "name: prod\n",
		"config/local.yaml": "name: local\n",
	})
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "app"}, // 默认 config/app.yaml
		{[]string{"--config", "config/app.yaml", "--config", "config/prod.yaml"}, "prod"},
		{[]string{"--config", "config/app.yaml,config/prod.yaml,config/local.yaml"}, "local"},
		{[]string{"--config", "config/prod.yaml", "--config", "config/app.yaml"}, "app"},
	} {
		if got := loadedName(t, tc.args...); got != tc.want {
			t.Errorf("args=%v name=%q want %q", tc.args, got, tc.want)
		}
	}
}

func TestDotEnvInWorkingDirectory(t *testing.T) {
	t.Setenv("TENON_TEST_NAME", "")
	os.Unsetenv("TENON_TEST_NAME")
	projectDir(t, map[string]string{
		".env":            "TENON_TEST_NAME=from-dotenv\n",
		"config/app.yaml": "name: ${TENON_TEST_NAME}\n",
	})
	if got := loadedName(t); got != "from-dotenv" {
		t.Fatalf("name=%q", got)
	}
}

func TestProcessEnvOverridesDotEnv(t *testing.T) {
	t.Setenv("TENON_TEST_NAME", "from-process")
	projectDir(t, map[string]string{
		".env":            "TENON_TEST_NAME=from-dotenv\n",
		"config/app.yaml": "name: ${TENON_TEST_NAME}\n",
	})
	if got := loadedName(t); got != "from-process" {
		t.Fatalf("name=%q", got)
	}
}

func TestInvalidDotEnvIsConfigError(t *testing.T) {
	projectDir(t, map[string]string{
		".env":            "BAD-KEY=value\n",
		"config/app.yaml": minimalConfig,
	})
	root := newRoot(command.Config[settings]())
	if _, _, err := execute(root, "config", "check"); command.ExitCode(err) != command.ExitConfig {
		t.Fatalf("exit=%d err=%v", command.ExitCode(err), err)
	}
}
