package scaffold_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/cmd/tenon/internal/scaffold"
)

func TestWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "todo")
	err := scaffold.Write(dir, scaffold.Options{Module: "example.com/todo", Name: "Todo", TenonVersion: "v0.0.0", TenonDir: "/src/tenon"})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{".gitignore", "go.mod", "package.json", "vite.config.ts", "tsconfig.json", "config/app.yaml",
		"config/local.yaml", "cmd/api/main.go", "internal/todo/todo.go", "frontend/frontend.go", "frontend/index.html",
		"frontend/src/main.tsx", "frontend/src/App.tsx", "frontend/src/client.ts"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	goMod, _ := os.ReadFile(filepath.Join(dir, "go.mod"))
	for _, want := range []string{"module example.com/todo\n", "go " + scaffold.GoVersion + "\n",
		"require github.com/go-tenon/tenon v0.0.0\n", "replace github.com/go-tenon/tenon => /src/tenon\n", "tool github.com/go-tenon/tenon/cmd/tenon\n"} {
		if !strings.Contains(string(goMod), want) {
			t.Errorf("go.mod missing %q:\n%s", want, goMod)
		}
	}
	main, _ := os.ReadFile(filepath.Join(dir, "cmd/api/main.go"))
	if !strings.Contains(string(main), `"example.com/todo/internal/todo"`) {
		t.Errorf("main.go:\n%s", main)
	}
	// 没有遗漏的模板标记；JSX 的 {{ }} 保持原样。
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			if strings.Contains(string(b), "[[") || strings.HasSuffix(p, ".tmpl") {
				t.Errorf("%s is not rendered", p)
			}
		}
		return err
	})
	if err := scaffold.Write(dir, scaffold.Options{Module: "x", Name: "x", TenonVersion: "v1"}); err == nil {
		t.Error("wrote into a non-empty directory")
	}
}

// 生成的 go.mod 要求的 Go 版本与框架相同。
func TestGoVersionMatchesFramework(t *testing.T) {
	b, err := os.ReadFile("../../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go (\S+)$`).FindSubmatch(b)
	if m == nil || string(m[1]) != scaffold.GoVersion {
		t.Fatalf("framework go %s, scaffold.GoVersion %s", m[1], scaffold.GoVersion)
	}
}
