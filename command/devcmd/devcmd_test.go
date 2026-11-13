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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/command/devcmd"
)

type settings struct {
	Addr string `yaml:"addr"`
}

// fake 是伪造的命令：记录参数；监听模式（--watch、run dev）一直运行，直到收到 SIGINT。
// go build -o BIN 写出一个伪造的应用：serve 一直运行到 SIGTERM，其他子命令记录后退出；
// 源码中有 BROKEN 时编译失败。
const fake = `#!/bin/sh
echo "$(basename "$0") $*" >> "$DEVCMD_LOG"
if [ "$1 $2" = "tool -n" ]; then echo /fake/templ; exit 0; fi
if [ "$1 $2" = "list -m" ]; then echo "$DEVCMD_FRAMEWORK"; exit 0; fi
if [ "$1 $2" = "build -o" ]; then
	if grep -rqs BROKEN --include=*.go .; then echo "main.go:1: syntax error" >&2; exit 1; fi
	cat > "$3" <<'APP'
#!/bin/sh
echo "app $* templ=$TEMPL_DEV_MODE" >> "$DEVCMD_LOG"
[ "$1" = "serve" ] || exit 0
trap 'echo "app stopped" >> "$DEVCMD_LOG"; exit 0' TERM
while :; do sleep 0.02; done
APP
	chmod +x "$3"
	exit 0
fi
case "$*" in
*--watch*|*"run dev"*)
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
	framework, _ := filepath.Abs("../..")
	t.Setenv("DEVCMD_FRAMEWORK", framework)
	t.Chdir(dir) // Tailwind 把 web/ui 的样式源复制到相对路径的入口旁边
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	if err := os.WriteFile(filepath.Join(dir, "app.yaml"), []byte("addr: 0.0.0.0:9090\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
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

func TestDevPages(t *testing.T) {
	dir, log := setup(t, "go", "tailwindcss", "shadcn-templ")
	if err := os.WriteFile(filepath.Join(dir, "layout.templ"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
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
		return strings.Contains(l, "go tool templ generate --watch") && strings.Contains(l, "tailwindcss -i") &&
			strings.Contains(l, "shadcn-templ bundle --watch") && strings.Contains(l, "app serve")
	})
	l := read(log)
	for _, want := range []string{
		"go tool -n templ",
		"--proxy=http://localhost:9090 --proxyport=" + port + " --open-browser=false\n",
		"app serve --config=" + filepath.Join(dir, "app.yaml") + " templ=true",
		"tailwindcss -i web/app.css -o web/static/app.css --watch=always",
	} {
		if !strings.Contains(l, want) {
			t.Errorf("log missing %q:\n%s", want, l)
		}
	}

	// 启动前，web/ui 的样式源已经复制到入口旁边，并且不会被提交。
	for _, f := range []string{"style-maia.css", "ui.css", "classes.txt", "vendor/styles/style-maia.css"} {
		if _, err := os.Stat(filepath.Join(dir, "web", devcmd.UIDir, f)); err != nil {
			t.Errorf("ui styles: %v", err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "web", devcmd.UIDir, ".gitignore")); string(b) != "*\n" {
		t.Errorf(".gitignore = %q", b)
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
	for _, want := range []string{"stopped go", "app stopped", "stopped tailwindcss", "stopped shadcn-templ", "go tool templ generate\n", "tailwindcss -i web/app.css -o web/static/app.css --minify", "shadcn-templ bundle\n"} {
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
	if err := os.WriteFile(filepath.Join(dir, "layout.templ"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
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
	waitFor(t, "app stopped", func() bool { return strings.Contains(read(log), "app stopped") })
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

// 单页应用：没有 .templ 文件时不运行 templ；Vite 与应用一起运行；改了 Go 代码后重新编译、重启并生成客户端；
// 编译失败时旧进程继续运行。
func TestDevSPA(t *testing.T) {
	defer devcmd.SetPollInterval(20 * time.Millisecond)()
	dir, log := setup(t, "go", "bun")
	if err := os.MkdirAll(filepath.Join(dir, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := command.Root("api", "")
	root.AddCommand(devcmd.New(devcmd.Options{
		Main:     "./cmd/api",
		Tools:    []devcmd.Tool{devcmd.Vite("frontend")},
		Generate: []string{"client", "--ts", "frontend/src/api"},
	}, func(s settings) string { return s.Addr }))
	var out, errOut syncBuffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs([]string{"dev", "--config", filepath.Join(dir, "app.yaml")})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- root.ExecuteContext(ctx) }()

	count := func(s string) int { return strings.Count(read(log), s) }
	waitFor(t, "app, generate and vite", func() bool {
		return count("app serve") == 1 && count("app client --ts frontend/src/api") == 1 && count("bun --cwd frontend run dev") == 1
	})
	l := read(log)
	// 没有 node_modules、dist 时先安装、构建一次；不检查也不运行 templ。
	for _, want := range []string{"bun --cwd frontend install", "bun --cwd frontend run build", "app serve --config=" + filepath.Join(dir, "app.yaml") + " templ=\n"} {
		if !strings.Contains(l, want) {
			t.Errorf("log missing %q:\n%s", want, l)
		}
	}
	if strings.Contains(l, "templ") && strings.Contains(l, "go tool") {
		t.Errorf("templ used without .templ files:\n%s", l)
	}

	// 修改 Go 代码：停止旧进程，启动新的，再生成一次客户端。
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart", func() bool {
		return count("app stopped") == 1 && count("app serve") == 2 && count("app client") == 2
	})
	// 编译失败：打印错误，旧进程继续运行。
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nBROKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "build failure", func() bool { return strings.Contains(errOut.String(), "编译失败") })
	if count("app stopped") != 1 || count("app serve") != 2 {
		t.Fatalf("app restarted after a failed build:\n%s", read(log))
	}
	// 测试文件和前端的变化不触发重新编译。
	before := count("go build")
	if err := os.WriteFile(filepath.Join(dir, "main_test.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", "app.tsx"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if n := count("go build"); n != before {
		t.Fatalf("rebuilt %d times for non-Go changes", n-before)
	}

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
	if !strings.Contains(l, "stopped bun") || count("app stopped") != 2 || count("bun --cwd frontend run build") != 2 {
		t.Errorf("after stop:\n%s", l)
	}
	if !strings.Contains(out.String(), "dev: 打开 http://localhost:9090") {
		t.Errorf("output = %q", out.String())
	}
}

func TestDevMissingBun(t *testing.T) {
	dir, _ := setup(t, "go")
	root := command.Root("api", "")
	root.AddCommand(devcmd.New(devcmd.Options{Main: "./cmd/api", Tools: []devcmd.Tool{devcmd.Vite("frontend")}}, func(s settings) string { return s.Addr }))
	root.SetOut(&bytes.Buffer{})
	root.SetArgs([]string{"dev", "--config", filepath.Join(dir, "app.yaml")})
	err := root.ExecuteContext(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bun not found") || command.ExitCode(err) != command.ExitUnavailable {
		t.Errorf("err = %v (exit %d)", err, command.ExitCode(err))
	}
}

// syncBuffer 是可以同时读写的 bytes.Buffer：dev 的子进程写入时，测试在读取。
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
