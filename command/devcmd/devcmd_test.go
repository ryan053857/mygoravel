//go:build unix

package devcmd_test

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/devcmd"
)

type settings struct {
	Addr string `yaml:"addr"`
}

// fake 是伪造的命令：记录参数；带 --watch 的一直运行，直到收到 SIGINT。
const fake = `#!/bin/sh
echo "$(basename "$0") $*" >> "$DEVCMD_LOG"
if [ "$1 $2" = "tool -n" ]; then echo /fake/templ; exit 0; fi
case "$*" in
*--watch*)
	trap 'echo "stopped $(basename "$0")" >> "$DEVCMD_LOG"; exit 0' INT
	while :; do sleep 0.02; done ;;
esac
`

func setup(t *testing.T, tools ...string) (dir, log string) {
	t.Helper()
	dir = t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log = filepath.Join(dir, "log")
	t.Setenv("DEVCMD_LOG", log)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("addr: 0.0.0.0:9090\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, log
}

func read(path string) string {
	b, _ := os.ReadFile(path)
	return string(b)
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestDev(t *testing.T) {
	dir, log := setup(t, "go", "tailwindcss", "shadcn-templ")
	static := filepath.Join(dir, "static")
	if err := os.Mkdir(static, 0o755); err != nil {
		t.Fatal(err)
	}
	// 伪造 templ 的代理，记录刷新通知。
	var reloads atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/_templ/reload/events" {
			reloads.Add(1)
		}
	}))
	defer proxy.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	proxyPort, _ := strconv.Atoi(port)

	root := command.Root("api", "")
	root.AddCommand(devcmd.New(devcmd.Options{
		Main:      "./cmd/api",
		ProxyPort: proxyPort,
		Static:    static,
		Tools: []devcmd.Tool{
			devcmd.Tailwind("web/app.css", "web/static/app.css"),
			{Watch: []string{"shadcn-templ", "bundle", "--watch"}, Build: []string{"shadcn-templ", "bundle"}},
		},
	}, func(s settings) string { return s.Addr }))
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs([]string{"dev", "--config", filepath.Join(dir, "app.yaml")})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- root.ExecuteContext(ctx) }()

	waitFor(t, "all watchers", func() bool {
		l := read(log)
		return strings.Contains(l, "go tool templ generate --watch") && strings.Contains(l, "tailwindcss -i") && strings.Contains(l, "shadcn-templ bundle --watch")
	})
	l := read(log)
	for _, want := range []string{
		"go tool -n templ",
		"--proxy=http://localhost:9090 --proxyport=" + port + " --open-browser=false --cmd=go run ./cmd/api serve --config=" + filepath.Join(dir, "app.yaml"),
		"tailwindcss -i web/app.css -o web/static/app.css --watch=always",
	} {
		if !strings.Contains(l, want) {
			t.Errorf("log missing %q:\n%s", want, l)
		}
	}

	if err := os.WriteFile(filepath.Join(static, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "browser reload", func() bool { return reloads.Load() >= 1 })

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("dev returned %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("dev did not stop")
	}
	l = read(log)
	for _, want := range []string{"stopped go", "stopped tailwindcss", "stopped shadcn-templ", "go tool templ generate\n", "tailwindcss -i web/app.css -o web/static/app.css --minify", "shadcn-templ bundle\n"} {
		if !strings.Contains(l, want) {
			t.Errorf("log missing %q after stop:\n%s", want, l)
		}
	}
	if !strings.Contains(out.String(), "http://localhost:"+port) {
		t.Errorf("output = %q", out.String())
	}
}

// 一个监听进程意外退出时，dev 停止其余进程并报错。
func TestDevStopsWhenAWatcherExits(t *testing.T) {
	dir, log := setup(t, "go", "tailwindcss")
	if err := os.WriteFile(filepath.Join(dir, "bin", "tailwindcss"), []byte("#!/bin/sh\nuntil grep -q 'templ generate --watch' \"$DEVCMD_LOG\"; do sleep 0.02; done\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := command.Root("api", "")
	root.AddCommand(devcmd.New(devcmd.Options{Main: "./cmd/api", Tools: []devcmd.Tool{devcmd.Tailwind("a.css", "b.css")}}, func(s settings) string { return s.Addr }))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"dev", "--config", filepath.Join(dir, "app.yaml")})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tailwindcss") {
		t.Errorf("err = %v", err)
	}
	if !strings.Contains(read(log), "stopped go") {
		t.Errorf("templ watcher not stopped:\n%s", read(log))
	}
}

func TestDevMissingTools(t *testing.T) {
	dir, _ := setup(t, "go")
	root := command.Root("api", "")
	root.AddCommand(devcmd.New(devcmd.Options{Main: "./cmd/api", Tools: []devcmd.Tool{devcmd.Tailwind("a.css", "b.css")}}, func(s settings) string { return s.Addr }))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"dev", "--config", filepath.Join(dir, "app.yaml")})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tailwindcss not found") || command.ExitCode(err) != command.ExitUnavailable {
		t.Errorf("err = %v (exit %d)", err, command.ExitCode(err))
	}
}
