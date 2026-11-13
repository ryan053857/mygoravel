package clientgen_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/httpx/rest/clientgen"
	"github.com/ryan053857/mygoravel/httpx/rest/clientgen/internal/example/article"
)

func runClient(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := command.Root("api", "test")
	root.AddCommand(clientgen.Command(rest.Options{Title: "Blog", BasePath: "/api"}, func(api *rest.API) {
		article.Register(api, article.NewStore(), func(op *huma.Operation) { rest.RequireBearer(api, tokens(nil).validate)(op) })
	}))
	var stderr bytes.Buffer
	root.SetErr(&stderr)
	root.SetOut(&bytes.Buffer{})
	root.SetArgs(args)
	err := root.ExecuteContext(context.Background())
	return stderr.String(), err
}

func TestCommandWritesClientAndRemovesStaleFiles(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "api")
	write := func(name, content string) {
		p := filepath.Join(out, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/blog\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	write("comment/comment.go", clientgen.Header+"\n\npackage comment\n")
	write("helpers.go", "package api\n")

	stderr, err := runClient(t, "client", "--out", out, "--main", exampleMain)
	if err != nil {
		t.Fatal(err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr %q", stderr)
	}
	data, err := os.ReadFile(filepath.Join(out, "article.go"))
	if err != nil || !strings.Contains(string(data), `"example.com/blog/api/article"`) {
		t.Fatalf("%s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(out, "comment")); !os.IsNotExist(err) {
		t.Fatal("stale generated package must be removed", err)
	}
	if _, err := os.Stat(filepath.Join(out, "helpers.go")); err != nil {
		t.Fatal("hand-written files must be kept", err)
	}
}

func TestCommandRequiresOut(t *testing.T) {
	_, err := runClient(t, "client")
	var exit *command.ExitError
	if !errors.As(err, &exit) || exit.Code != command.ExitUsage {
		t.Fatalf("%#v", err)
	}
}

// --ts 生成 TypeScript 客户端，删除上次生成、这次不再生成的 .ts 文件，保留手写的文件。
func TestCommandWritesTSClient(t *testing.T) {
	out := filepath.Join(t.TempDir(), "api")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"old.ts": clientgen.Header + "\n", "query.ts": "export {}\n"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stderr, err := runClient(t, "client", "--ts", out, "--main", exampleMain)
	if err != nil || stderr != "" {
		t.Fatal(err, stderr)
	}
	for _, name := range []string{"api.ts", "errors.ts", "runtime.ts", "query.ts"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "old.ts")); !os.IsNotExist(err) {
		t.Error("stale generated file must be removed", err)
	}
}
