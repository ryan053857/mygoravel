package dev

import (
	"github.com/go-tenon/tenon/cmd/tenon/internal/project"

	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHostPort(t *testing.T) {
	for raw, want := range map[string]string{
		"http://localhost:8080":   "localhost:8080",
		"http://127.0.0.1":        "127.0.0.1:80",
		"https://dev.example.com": "dev.example.com:443",
		"http://[::1]:9000":       "[::1]:9000",
	} {
		if got := hostPort(raw); got != want {
			t.Errorf("hostPort(%q) = %q", raw, got)
		}
	}
}

func TestWatchersAndBuilds(t *testing.T) {
	cfg := &project.Config{
		URL:      "http://localhost:8080",
		Frontend: &project.Frontend{Dir: "frontend", DevURL: "http://localhost:5173"},
		Templ:    &project.Templ{ProxyPort: 7331},
		Tailwind: &project.Tailwind{Input: "web/app.css", Output: "web/static/app.css"},
		Tools:    []project.Tool{{Watch: []string{"esbuild", "--watch"}, Build: []string{"esbuild"}}},
	}
	t.Chdir(t.TempDir()) // 没有根目录的 package.json：在前端目录中运行 vite
	d := dev{cfg: cfg}
	want := []watcher{
		{"frontend", []string{"bun", "x", "vite", "--port", "5173", "--strictPort"}},
		{".", []string{"go", "tool", "templ", "generate", "--watch", "--proxy=http://localhost:8080", "--proxyport=7331", "--open-browser=false"}},
		{".", []string{"tailwindcss", "-i", "web/app.css", "-o", "web/static/app.css", "--watch=always"}},
		{".", []string{"esbuild", "--watch"}},
	}
	if got := d.watchers(); !slices.EqualFunc(got, want, func(a, b watcher) bool { return a.dir == b.dir && slices.Equal(a.args, b.args) }) {
		t.Errorf("watchers = %q", got)
	}
	wantBuilds := []watcher{
		{".", []string{"go", "tool", "templ", "generate"}},
		{"frontend", []string{"bun", "x", "vite", "build"}},
		{".", []string{"tailwindcss", "-i", "web/app.css", "-o", "web/static/app.css", "--minify"}},
		{".", []string{"esbuild"}},
	}
	if got := d.builds(); !slices.EqualFunc(got, wantBuilds, func(a, b watcher) bool { return a.dir == b.dir && slices.Equal(a.args, b.args) }) {
		t.Errorf("builds = %q", got)
	}
	// 根目录有 package.json 时在根目录运行 vite。
	if err := os.WriteFile("package.json", []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if d.watchers()[0].dir != "." {
		t.Error("vite not run in the project root")
	}
	if got := GenerateArgs(&project.Config{Generate: &project.Generate{TS: "frontend/src/api", Go: "../api"}}); strings.Join(got, " ") != "client --ts frontend/src/api --out ../api" {
		t.Errorf("generate args %q", got)
	}
	if GenerateArgs(&project.Config{Generate: &project.Generate{}}) != nil {
		t.Error("generate without targets")
	}
}

// 应用的源码：.go（测试除外）、go.mod、go.sum；跳过隐藏目录、node_modules、testdata、dist。
func TestGoSourceSnapshot(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"main.go", "go.mod", "go.sum", "a_test.go", "README.md", "internal/x.go",
		".git/x.go", "frontend/node_modules/y.go", "testdata/z.go", "frontend/dist/w.go", "frontend/frontend.go"} {
		path := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	snap := snapshot(dir, goSource)
	var files []string
	for _, line := range strings.Split(strings.TrimSpace(snap), "\n") {
		rel, _ := filepath.Rel(dir, strings.Fields(line)[0])
		files = append(files, rel)
	}
	want := []string{"frontend/frontend.go", "go.mod", "go.sum", "internal/x.go", "main.go"}
	if !slices.Equal(files, want) {
		t.Errorf("files = %q", files)
	}
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	state := func() string { return snapshot(filepath.Join(dir, "static"), nil) }
	go func() { done <- watch(ctx, state, 10*time.Millisecond, func() { n.Add(1) }) }()

	time.Sleep(40 * time.Millisecond)
	if n.Load() != 0 {
		t.Fatal("changed without a change")
	}
	// 目录稍后才出现：新文件算一次变化。
	if err := os.MkdirAll(filepath.Join(dir, "static", "css"), 0o755); err != nil {
		t.Fatal(err)
	}
	css := filepath.Join(dir, "static", "css", "app.css")
	if err := os.WriteFile(css, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	// WriteFile 先清空再写入，轮询可能落在两步之间，把一次写入算成两次变化。
	waitUntil(t, func() bool { return n.Load() >= 1 })
	before := n.Load()
	if err := os.WriteFile(css, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return n.Load() > before })
	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("watch returned %v", err)
	}
}

func waitUntil(t *testing.T, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal("timed out")
}
