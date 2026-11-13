//go:build unix

package dev_test

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

	"github.com/go-tenon/tenon/cmd/tenon/internal/dev"
	"github.com/go-tenon/tenon/cmd/tenon/internal/project"
)

// fake 是伪造的命令：记录参数；监听模式（--watch、bun x vite）一直运行，直到收到 SIGINT。
// go build -o BIN 写出一个伪造的应用：serve 一直运行到 SIGTERM，其他子命令记录后退出；源码中有 BROKEN 时编译失败。
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
*--watch*|"x vite")
	trap 'echo "stopped $(basename "$0")" >> "$DEVCMD_LOG"; exit 0' INT
	while :; do sleep 0.02; done ;;
esac
`

// setup 在临时目录中准备一个项目和伪造的命令，返回目录和记录命令的日志。
func setup(t *testing.T, tools ...string) (dir, log string) {
	t.Helper()
	dir = t.TempDir()
	bin := filepath.Join(dir, ".bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range tools {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	log = filepath.Join(dir, ".log")
	t.Setenv("DEVCMD_LOG", log)
	framework, _ := filepath.Abs("../../../..")
	t.Setenv("DEVCMD_FRAMEWORK", framework)
	t.Chdir(dir)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
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

func load(t *testing.T) *project.Config {
	t.Helper()
	cfg, err := project.Load(".")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// start 运行 dev.Run，返回停止它并等待返回的函数。
func start(t *testing.T, cfg *project.Config, out, errOut *syncBuffer) func() error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- dev.Run(ctx, cfg, out, errOut) }()
	return func() error {
		cancel()
		select {
		case err := <-done:
			return err
		case <-time.After(10 * time.Second):
			t.Fatal("dev did not stop")
			return nil
		}
	}
}

// 服务端渲染的页面：templ 不带 --cmd，应用带 TEMPL_DEV_MODE；静态文件变化时刷新浏览器；Tailwind 先复制样式源。
func TestDevPages(t *testing.T) {
	dir, log := setup(t, "go", "tailwindcss", "esbuild")
	var reloads atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/_templ/reload/events" {
			reloads.Add(1)
		}
	}))
	defer proxy.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(proxy.URL, "http://"))
	proxyPort, _ := strconv.Atoi(port)
	static := filepath.Join(dir, "static")
	if err := os.Mkdir(static, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "layout.templ"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	cfg.Templ.ProxyPort, cfg.Templ.Static = proxyPort, "static"
	cfg.Tailwind = &project.Tailwind{Input: "web/app.css", Output: "web/static/app.css"}
	cfg.Tools = []project.Tool{{Watch: []string{"esbuild", "--watch"}, Build: []string{"esbuild", "--minify"}}}
	var out, errOut syncBuffer
	stop := start(t, cfg, &out, &errOut)

	waitFor(t, "all watchers", func() bool {
		l := read(log)
		return strings.Contains(l, "go tool templ generate --watch") && strings.Contains(l, "tailwindcss -i") &&
			strings.Contains(l, "esbuild --watch") && strings.Contains(l, "app serve")
	})
	l := read(log)
	for _, want := range []string{
		"go tool -n templ",
		"--proxy=http://localhost:8080 --proxyport=" + port + " --open-browser=false\n",
		"app serve --config config/app.yaml templ=true",
		"tailwindcss -i web/app.css -o web/static/app.css --watch=always",
	} {
		if !strings.Contains(l, want) {
			t.Errorf("log missing %q:\n%s", want, l)
		}
	}
	for _, f := range []string{"style-maia.css", "ui.css", "classes.txt"} {
		if _, err := os.Stat(filepath.Join(dir, "web", dev.UIDir, f)); err != nil {
			t.Errorf("ui styles: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(static, "app.css"), []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "browser reload", func() bool { return reloads.Load() >= 1 })

	if err := stop(); err != nil {
		t.Fatalf("dev returned %v", err)
	}
	l = read(log)
	for _, want := range []string{"stopped go", "app stopped", "stopped tailwindcss", "stopped esbuild", "go tool templ generate\n", "--minify"} {
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
	dir, log := setup(t, "go", "esbuild")
	if err := os.WriteFile(filepath.Join(dir, ".bin", "esbuild"), []byte("#!/bin/sh\nuntil grep -q 'app serve' \"$DEVCMD_LOG\"; do sleep 0.02; done\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	cfg.Tools = []project.Tool{{Watch: []string{"esbuild", "--watch"}}}
	err := dev.Run(context.Background(), cfg, &bytes.Buffer{}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "esbuild") {
		t.Errorf("err = %v", err)
	}
	waitFor(t, "app stopped", func() bool { return strings.Contains(read(log), "app stopped") })
}

func TestDevMissingTools(t *testing.T) {
	setup(t, "go")
	cfg := load(t)
	for name, edit := range map[string]func(*project.Config){
		"tailwindcss not found": func(c *project.Config) { c.Tailwind = &project.Tailwind{Input: "a", Output: "b"} },
		"bun not found":         func(c *project.Config) { c.Frontend = &project.Frontend{Dir: "frontend"} },
	} {
		c := *cfg
		edit(&c)
		if err := dev.Run(context.Background(), &c, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// 单页应用：没有 node_modules、dist 时先安装、构建；运行 vite 和应用；改了 Go 代码后重新编译、重启并生成客户端；
// 编译失败时旧进程继续运行；测试文件和前端的变化不触发重新编译。
func TestDevSPA(t *testing.T) {
	defer dev.SetPollInterval(20 * time.Millisecond)()
	dir, log := setup(t, "go", "bun")
	if err := os.MkdirAll(filepath.Join(dir, "frontend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frontend", "index.html"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := load(t)
	vite := httptest.NewServer(http.NotFoundHandler()) // 代替 Vite 的开发服务器：dev 等它能访问后再启动应用
	defer vite.Close()
	cfg.Frontend.DevURL = vite.URL
	var out, errOut syncBuffer
	stop := start(t, cfg, &out, &errOut)

	count := func(s string) int { return strings.Count(read(log), s) }
	waitFor(t, "app, generate and vite", func() bool {
		return count("app serve") == 1 && count("app client --ts frontend/src/api") == 1 && count("bun x vite\n") == 1
	})
	l := read(log)
	for _, want := range []string{"bun install\n", "bun x vite build\n", "app serve --config config/app.yaml templ=\n"} {
		if !strings.Contains(l, want) {
			t.Errorf("log missing %q:\n%s", want, l)
		}
	}
	if strings.Contains(l, "go tool") {
		t.Errorf("templ used without .templ files:\n%s", l)
	}

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "restart", func() bool { return count("app stopped") == 1 && count("app serve") == 2 && count("app client") == 2 })
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nBROKEN\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "build failure", func() bool { return strings.Contains(errOut.String(), "编译失败") })
	if count("app stopped") != 1 || count("app serve") != 2 {
		t.Fatalf("app restarted after a failed build:\n%s", read(log))
	}
	before := count("go build")
	for _, f := range []string{"main_test.go", "frontend/app.tsx", "node_modules/x/x.go"} {
		p := filepath.Join(dir, f)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if n := count("go build"); n != before {
		t.Fatalf("rebuilt %d times for non-Go changes", n-before)
	}

	if err := stop(); err != nil {
		t.Fatalf("dev returned %v", err)
	}
	l = read(log)
	if !strings.Contains(l, "stopped bun") || count("app stopped") != 2 || count("bun x vite build") != 2 {
		t.Errorf("after stop:\n%s", l)
	}
	if !strings.Contains(out.String(), "dev: 打开 http://localhost:8080") {
		t.Errorf("output = %q", out.String())
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
