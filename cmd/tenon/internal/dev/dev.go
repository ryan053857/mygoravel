// Package dev 实现 tenon dev：与应用一起运行 Vite、templ、Tailwind 和其他工具，修改 Go 代码后重新编译、重启应用，
// 并重新生成客户端。
package dev

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/cmd/tenon/internal/project"
)

// UIDir 是 Tailwind 在样式入口旁边放置 web/ui 样式源的目录名。
const UIDir = ".web-ui"

const framework = "github.com/go-tenon/tenon"

// Run 在当前目录运行开发模式，直到 ctx 取消；停止后生成正式的代码、样式表和前端构建产物。
func Run(ctx context.Context, cfg *project.Config, out, errOut io.Writer) error {
	d := dev{cfg: cfg, out: shared(out), errOut: shared(errOut)}
	if err := d.check(); err != nil {
		return err
	}
	if err := d.prepare(); err != nil {
		return err
	}
	if cfg.Templ != nil {
		fmt.Fprintf(d.out, "dev: 打开 http://localhost:%d（代理 %s）\n", cfg.Templ.ProxyPort, cfg.URL)
	} else {
		fmt.Fprintf(d.out, "dev: 打开 %s\n", cfg.URL)
	}
	err := concurrently(ctx, d.runs()...)
	// 停止后恢复正式的生成结果：templ 的 watch 模式生成的代码读取 _templ.txt，工具的输出也没有压缩。
	return errors.Join(err, d.finish())
}

type dev struct {
	cfg         *project.Config
	out, errOut io.Writer
}

// shared 让多个子进程可以同时写 w。*os.File 直接交给子进程，它们能识别终端、输出颜色。
func shared(w io.Writer) io.Writer {
	if _, ok := w.(*os.File); ok {
		return w
	}
	return &lockedWriter{w: w}
}

type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// FrontendRoot 返回运行 Vite 的目录：项目根目录有 package.json 时是它，否则是前端目录。
func FrontendRoot(cfg *project.Config) string {
	if _, err := os.Stat("package.json"); err == nil {
		return "."
	}
	return cfg.Frontend.Dir
}

// check 确认用到的命令都已安装：go；有前端时 bun；有 templ 时 go.mod 中的 templ 工具；Tailwind 和其他工具。
func (d dev) check() error {
	need := []string{"go"}
	if d.cfg.Frontend != nil {
		need = append(need, "bun")
	}
	if d.cfg.Tailwind != nil {
		need = append(need, "tailwindcss")
	}
	for _, t := range d.cfg.Tools {
		need = append(need, t.Watch[0])
		if len(t.Build) > 0 {
			need = append(need, t.Build[0])
		}
	}
	for _, name := range need {
		if _, err := exec.LookPath(name); err != nil {
			switch name {
			case "bun":
				return errors.New("dev: bun not found in PATH, install it from https://bun.sh")
			case "tailwindcss":
				return errors.New("dev: tailwindcss not found in PATH, download the standalone CLI from https://github.com/tailwindlabs/tailwindcss/releases")
			}
			return errors.Newf("dev: %s not found in PATH", name)
		}
	}
	if d.cfg.Templ != nil {
		if out, err := exec.Command("go", "tool", "-n", "templ").CombinedOutput(); err != nil {
			return errors.Newf("dev: templ is not a tool of this module, run: go get -tool github.com/a-h/templ/cmd/templ\n%s", out)
		}
	}
	return nil
}

// prepare 在启动监听进程之前运行一次：没有 node_modules 时 bun install，没有 dist 时先构建前端
// （否则嵌入 dist 的代码不能编译）；把框架 web/ui 的样式源复制到 Tailwind 入口旁边。
func (d dev) prepare() error {
	if f := d.cfg.Frontend; f != nil {
		root := FrontendRoot(d.cfg)
		if _, err := os.Stat(filepath.Join(root, "node_modules")); errors.Is(err, fs.ErrNotExist) {
			if err := d.exec(root, "bun", "install"); err != nil {
				return err
			}
		}
		if _, err := os.Stat(filepath.Join(f.Dir, "dist")); errors.Is(err, fs.ErrNotExist) {
			if err := d.exec(root, "bun", "x", "vite", "build"); err != nil {
				return err
			}
		}
	}
	if t := d.cfg.Tailwind; t != nil {
		return copyUI(filepath.Join(filepath.Dir(t.Input), UIDir))
	}
	return nil
}

func (d dev) exec(dir string, args ...string) error {
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = d.out, d.errOut
	return errors.Wrapf(cmd.Run(), "dev: %s", strings.Join(args, " "))
}

// watcher 是一个与应用一起运行的监听进程。
type watcher struct {
	dir  string
	args []string
}

// watchers 返回与应用一起运行的监听进程。
func (d dev) watchers() []watcher {
	var ws []watcher
	if d.cfg.Frontend != nil {
		// 直接运行 vite，不经过 package.json 的 dev 脚本：那个名字留给 tenon dev 自己。
		ws = append(ws, watcher{FrontendRoot(d.cfg), []string{"bun", "x", "vite"}})
	}
	if t := d.cfg.Templ; t != nil {
		ws = append(ws, watcher{".", []string{"go", "tool", "templ", "generate", "--watch",
			"--proxy=" + d.cfg.URL, "--proxyport=" + strconv.Itoa(t.ProxyPort), "--open-browser=false"}})
	}
	if t := d.cfg.Tailwind; t != nil {
		// --watch=always：标准输入不是终端时也继续监听，否则 tailwindcss 在标准输入关闭时退出。
		ws = append(ws, watcher{".", []string{"tailwindcss", "-i", t.Input, "-o", t.Output, "--watch=always"}})
	}
	for _, t := range d.cfg.Tools {
		ws = append(ws, watcher{".", t.Watch})
	}
	return ws
}

// builds 返回停止后运行的生成命令。
func (d dev) builds() []watcher {
	var bs []watcher
	if d.cfg.Templ != nil {
		bs = append(bs, watcher{".", []string{"go", "tool", "templ", "generate"}})
	}
	if d.cfg.Frontend != nil {
		bs = append(bs, watcher{FrontendRoot(d.cfg), []string{"bun", "x", "vite", "build"}})
	}
	if t := d.cfg.Tailwind; t != nil {
		bs = append(bs, watcher{".", []string{"tailwindcss", "-i", t.Input, "-o", t.Output, "--minify"}})
	}
	for _, t := range d.cfg.Tools {
		if len(t.Build) > 0 {
			bs = append(bs, watcher{".", t.Build})
		}
	}
	return bs
}

func (d dev) runs() []func(context.Context) error {
	runs := []func(context.Context) error{d.serve}
	for _, w := range d.watchers() {
		runs = append(runs, func(ctx context.Context) error { return d.run(ctx, w) })
	}
	if t := d.cfg.Templ; t != nil && t.Static != "" {
		runs = append(runs, func(ctx context.Context) error {
			return watch(ctx, func() string { return snapshot(t.Static, nil) }, 250*time.Millisecond, d.reload)
		})
	}
	return runs
}

// run 运行一个监听进程，ctx 取消时向它的整个进程组发送中断信号。
func (d dev) run(ctx context.Context, w watcher) error {
	cmd := exec.CommandContext(ctx, w.args[0], w.args[1:]...)
	cmd.Dir = w.dir
	cmd.Stdout, cmd.Stderr = d.out, d.errOut
	interruptGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Wrapf(err, "dev: %s", strings.Join(w.args, " "))
}

// reload 通知 templ 的代理刷新浏览器。
func (d dev) reload() {
	url := fmt.Sprintf("http://127.0.0.1:%d/_templ/reload/events", d.cfg.Templ.ProxyPort)
	if res, err := http.Post(url, "", nil); err == nil {
		_ = res.Body.Close()
	}
}

// finish 运行停止后的生成命令。
func (d dev) finish() error {
	for _, b := range d.builds() {
		if err := d.exec(b.dir, b.args...); err != nil {
			return err
		}
	}
	return nil
}

// concurrently 同时运行 fns，任何一个返回时取消其余的，返回第一个不是 context.Canceled 的错误。
func concurrently(ctx context.Context, fns ...func(context.Context) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, len(fns))
	for _, fn := range fns {
		go func() { errs <- fn(ctx) }()
	}
	var first error
	for range fns {
		err := <-errs
		cancel()
		if first == nil && err != nil && !errors.Is(err, context.Canceled) {
			first = err
		}
	}
	return first
}

// watch 每隔 every 取一次 state，变化并且稳定下来（连续两次相同）后调用 changed。
func watch(ctx context.Context, state func() string, every time.Duration, changed func()) error {
	last := state()
	t := time.NewTicker(every)
	defer t.Stop()
	pending := false
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			cur := state()
			switch {
			case cur != last:
				last, pending = cur, true
			case pending:
				pending = false
				changed()
			}
		}
	}
}

// snapshot 返回 dir 中文件的路径、大小和修改时间；include 不为 nil 时只算它接受的文件。目录不存在时为空。
func snapshot(dir string, include func(path string) bool) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if path != dir && project.SkipDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if include != nil && !include(path) {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}

// goSource 报告 path 是否影响应用的编译：.go（测试除外）、go.mod、go.sum。
func goSource(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go") || base == "go.mod" || base == "go.sum"
}

// copyUI 把框架的 web/ui/css 复制到 dst，替换已有的内容。框架的位置由 go list 给出（通常在模块缓存中）。
func copyUI(dst string) error {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", framework).Output()
	dir := strings.TrimSpace(string(out))
	if err != nil || dir == "" {
		return errors.Newf("devcmd: cannot locate %s with go list: %v", framework, err)
	}
	src := filepath.Join(dir, "web", "ui", "css")
	if err := os.RemoveAll(dst); err != nil {
		return errors.Wrap(err, "devcmd: remove ui styles")
	}
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, 0o644)
	})
	if err != nil {
		return errors.Wrap(err, "devcmd: copy ui styles")
	}
	return errors.Wrap(os.WriteFile(filepath.Join(dst, ".gitignore"), []byte("*\n"), 0o644), "devcmd: copy ui styles")
}
