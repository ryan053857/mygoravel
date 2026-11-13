package devcmd

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyTarget(t *testing.T) {
	for addr, want := range map[string]string{
		":8080":          "http://localhost:8080",
		"0.0.0.0:8080":   "http://localhost:8080",
		"127.0.0.1:3000": "http://127.0.0.1:3000",
		"[::]:8080":      "http://localhost:8080",
		"[::1]:8080":     "http://[::1]:8080",
	} {
		if got, err := proxyTarget(addr); err != nil || got != want {
			t.Errorf("proxyTarget(%q) = %q, %v", addr, got, err)
		}
	}
	if _, err := proxyTarget("8080"); err == nil {
		t.Error("address without port accepted")
	}
}

func TestCommands(t *testing.T) {
	d := dev{
		opts: Options{
			Main: "./cmd/api", Serve: "serve", ProxyPort: 7331,
			Tools: []Tool{
				Tailwind("web/app.css", "web/static/app.css"),
				{Watch: []string{"shadcn-templ", "bundle", "--watch"}, Build: []string{"shadcn-templ", "bundle"}},
			},
		},
		configs: []string{"config/app.yaml"},
		target:  "http://localhost:8080",
		templ:   true,
	}
	want := [][]string{
		{"go", "tool", "templ", "generate", "--watch", "--proxy=http://localhost:8080", "--proxyport=7331", "--open-browser=false"},
		{"tailwindcss", "-i", "web/app.css", "-o", "web/static/app.css", "--watch=always"},
		{"shadcn-templ", "bundle", "--watch"},
	}
	if cmds := d.commands(); !slices.EqualFunc(cmds, want, slices.Equal) {
		t.Errorf("commands = %q", cmds)
	}
	// 没有 .templ 文件时不运行 templ。
	d.templ, d.opts.Tools = false, []Tool{Vite("frontend")}
	if cmds := d.commands(); !slices.EqualFunc(cmds, [][]string{{"bun", "--cwd", "frontend", "run", "dev"}}, slices.Equal) {
		t.Errorf("spa commands = %q", cmds)
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
	if hasTempl(dir) {
		t.Error("found templ without .templ files")
	}
	if err := os.WriteFile(filepath.Join(dir, "internal", "page.templ"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if !hasTempl(dir) {
		t.Error("templ file not found")
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
