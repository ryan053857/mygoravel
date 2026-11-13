//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake 记录参数；go build -o BIN 写出一个记录参数的伪造应用。
const fake = `#!/bin/sh
echo "$(basename "$0") $* @$(basename "$PWD")" >> "$TENON_LOG"
if [ "$1" = "build" ] && [ "$2" = "-o" ]; then
	printf '#!/bin/sh\necho "app $*" >> "$TENON_LOG"\n' > "$3"; chmod +x "$3"
fi
`

func newProject(t *testing.T, files map[string]string, tools ...string) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, ".bin")
	_ = os.Mkdir(bin, 0o755)
	for _, name := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(dir, ".log")
	t.Setenv("TENON_LOG", log)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	t.Chdir(dir)
	return log
}

func execute(t *testing.T, args ...string) error {
	t.Helper()
	cmd := root()
	cmd.SetArgs(args)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	return cmd.Execute()
}

func TestBuild(t *testing.T) {
	log := newProject(t, map[string]string{"frontend/index.html": "", "package.json": "{}"}, "go", "bun")
	if err := execute(t, "build"); err != nil {
		t.Fatal(err)
	}
	want := "bun install --frozen-lockfile @" // 根目录有 package.json：在根目录安装和构建
	l, _ := os.ReadFile(log)
	lines := strings.Split(strings.TrimSpace(string(l)), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], want) || !strings.HasPrefix(lines[1], "bun x vite build @") ||
		!strings.HasPrefix(lines[2], "go build -trimpath -o bin/api ./cmd/api @") {
		t.Fatalf("log:\n%s", l)
	}
}

func TestBuildWithoutFrontend(t *testing.T) {
	log := newProject(t, map[string]string{"tenon.json": `{"main": "./cmd/web"}`}, "go")
	if err := execute(t, "build", "-o", "out/web"); err != nil {
		t.Fatal(err)
	}
	if l, _ := os.ReadFile(log); !strings.HasPrefix(string(l), "go build -trimpath -o out/web ./cmd/web") || strings.Count(string(l), "\n") != 1 {
		t.Fatalf("log:\n%s", l)
	}
}

func TestGenerate(t *testing.T) {
	log := newProject(t, map[string]string{"frontend/index.html": "", "tenon.json": `{"generate": {"go": "../api"}}`}, "go")
	if err := execute(t, "generate"); err != nil {
		t.Fatal(err)
	}
	if l, _ := os.ReadFile(log); !strings.Contains(string(l), "app client --ts frontend/src/api --out ../api") {
		t.Fatalf("log:\n%s", l)
	}
	newProject(t, nil, "go")
	if err := execute(t, "generate"); err == nil || !strings.Contains(err.Error(), "nothing to generate") {
		t.Fatalf("err = %v", err)
	}
}
