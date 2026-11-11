package devcmd

import (
	"context"
	"os"
	"os/exec"
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

// serveCommand 由 templ 交给 shell 执行：带空格、引号的配置路径要原样传到应用。
func TestServeCommandQuoting(t *testing.T) {
	d := dev{opts: Options{Main: "./cmd/api", Serve: "serve"}, configs: []string{"config/app.yaml", "my config/it's.yaml"}}
	cmd := d.serveCommand()
	if !strings.HasPrefix(cmd, "go run ./cmd/api serve --config=config/app.yaml ") {
		t.Errorf("command = %s", cmd)
	}
	out, err := exec.Command("/bin/sh", "-c", strings.Replace(cmd, "go run", "printf '%s\\n'", 1)).Output()
	if err != nil {
		t.Skip("no /bin/sh")
	}
	want := "./cmd/api\nserve\n--config=config/app.yaml\n--config=my config/it's.yaml\n"
	if string(out) != want {
		t.Errorf("shell saw %q", out)
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
	}
	cmds := d.commands()
	want := [][]string{
		{"go", "tool", "templ", "generate", "--watch", "--proxy=http://localhost:8080", "--proxyport=7331", "--open-browser=false", "--cmd=go run ./cmd/api serve --config=config/app.yaml"},
		{"tailwindcss", "-i", "web/app.css", "-o", "web/static/app.css", "--watch=always"},
		{"shadcn-templ", "bundle", "--watch"},
	}
	if !slices.EqualFunc(cmds, want, slices.Equal) {
		t.Errorf("commands = %q", cmds)
	}
	d.opts.Tools = nil
	if n := len(d.commands()); n != 1 {
		t.Errorf("without tools: %d commands", n)
	}
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	var n atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- watch(ctx, filepath.Join(dir, "static"), 10*time.Millisecond, func() { n.Add(1) }) }()

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
	waitUntil(t, func() bool { return n.Load() == 1 })
	if err := os.WriteFile(css, []byte("ab"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return n.Load() == 2 })
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
