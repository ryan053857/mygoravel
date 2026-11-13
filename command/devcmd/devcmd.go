// Package devcmd 提供 dev 命令：开发时一条命令运行应用和全部监听进程，修改 Go 代码后重新编译、重启应用。
//
// 单页应用（React 等，见 httpx.SPA）：
//
//	root.AddCommand(devcmd.New(devcmd.Options{
//		Main:     "./cmd/api",
//		Tools:    []devcmd.Tool{devcmd.Vite("frontend")},
//		Generate: []string{"client", "--ts", "frontend/src/api"},
//	}, func(cfg settings.Config) string { return cfg.HTTP.Addr }))
//
// 服务端渲染的页面（模块中有 .templ 文件时）还会运行 templ generate --watch，通过代理刷新浏览器：
//
//	root.AddCommand(devcmd.New(devcmd.Options{
//		Main:   "./cmd/api",
//		Static: "internal/web/static",
//		Tools:  []devcmd.Tool{devcmd.Tailwind("internal/web/app.css", "internal/web/static/css/app.css")},
//	}, func(cfg settings.Config) string { return cfg.HTTP.Addr }))
//
// 然后运行 go run ./cmd/api dev --config config/app.yaml,config/local.yaml。
package devcmd

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"

	"github.com/go-tenon/tenon/command"
)

// Options 配置 dev 命令。
type Options struct {
	// Main 是应用入口的包路径，例如 ./cmd/api。必填。
	Main string
	// Serve 是启动 HTTP 服务的子命令，默认 serve。
	Serve string
	// Generate 是每次重新编译成功后，用编译出的程序运行一次的子命令，例如生成 TypeScript 客户端：
	// []string{"client", "--ts", "frontend/src/api"}。接口改了，前端的类型随之更新。可空。
	Generate []string
	// Tools 是与应用一起运行的工具，例如 Vite、Tailwind。
	Tools []Tool
	// ProxyPort 是服务端渲染页面时浏览器访问的端口，默认 7331：templ 在这里代理应用，并注入自动刷新的脚本。
	// 模块中没有 .templ 文件时不使用，浏览器直接打开应用的地址。
	ProxyPort int
	// Static 是服务端渲染页面的静态文件目录，例如 internal/web/static。其中的文件变化时刷新浏览器，
	// 例如 Tailwind 重新生成了样式表。为空时不监视；没有 .templ 文件时不使用。
	Static string
}

// Tool 是一个与应用一起运行的工具：开发时运行 Watch，dev 停止后运行一次 Build。
// 监听模式的输出通常没有压缩，Build 把它恢复为正式版本。
//
//	devcmd.Tool{Watch: []string{"esbuild", "js/app.js", "--outdir=static/js", "--watch"}, Build: []string{"esbuild", "js/app.js", "--outdir=static/js", "--minify"}}
type Tool struct {
	Watch []string
	Build []string // 可空
	// Prepare 在启动 Watch 之前运行一次，可空。
	Prepare func() error
}

// Vite 返回 dir 中的 Vite 项目，用 Bun 运行它的 package.json 脚本：开发时 bun run dev，停止后 bun run build。
// 应用用 httpx.SPA 嵌入 dir/dist，并把 SPAOptions.DevServer 设为 Vite 的地址，浏览器打开应用的地址就能热更新。
//
// 启动前，没有 node_modules 时先运行 bun install；没有 dist 时先构建一次，go:embed 才能编译通过。
func Vite(dir string) Tool {
	bun := func(args ...string) []string { return append([]string{"bun", "--cwd", dir}, args...) }
	return Tool{
		Watch: bun("run", "dev"),
		Build: bun("run", "build"),
		Prepare: func() error {
			for _, step := range []struct {
				missing string
				args    []string
			}{{"node_modules", bun("install")}, {"dist", bun("run", "build")}} {
				if _, err := os.Stat(filepath.Join(dir, step.missing)); !errors.Is(err, fs.ErrNotExist) {
					continue
				}
				cmd := exec.Command(step.args[0], step.args[1:]...)
				cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
				if err := cmd.Run(); err != nil {
					return errors.Wrapf(err, "devcmd: %s", strings.Join(step.args, " "))
				}
			}
			return nil
		},
	}
}

// UIDir 是 Tailwind 在样式入口旁边放置 web/ui 样式源的目录名。
const UIDir = ".web-ui"

// Tailwind 返回 Tailwind standalone CLI：监听时生成 output，停止后生成压缩的 output。
//
// 启动前，它把框架 web/ui 的样式源复制到 input 旁边的 UIDir 目录（目录中带 .gitignore，不会被提交），
// 入口用相对路径导入一套风格，组件、风格与应用自己的类名生成在同一份样式表中：
//
//	@import "tailwindcss";
//	@import "./.web-ui/style-maia.css";
func Tailwind(input, output string) Tool {
	// --watch=always：标准输入不是终端时也继续监听，否则 tailwindcss 在标准输入关闭时退出。
	return Tool{
		Watch:   []string{"tailwindcss", "-i", input, "-o", output, "--watch=always"},
		Build:   []string{"tailwindcss", "-i", input, "-o", output, "--minify"},
		Prepare: func() error { return copyUI(filepath.Join(filepath.Dir(input), UIDir)) },
	}
}

const framework = "github.com/go-tenon/tenon"

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

// New 构造 dev 命令。它加载 --config 指定的配置，用 addr 取应用的 HTTP 监听地址，然后同时运行：
//
//   - 应用：go build Main 到临时目录，运行 Serve（带上相同的 --config）。模块中的 .go 文件、go.mod、go.sum
//     变化后重新编译；成功时平滑停止旧进程（SIGTERM）、启动新的，再运行 Generate；编译失败时打印错误，旧进程继续运行；
//   - Tools 中每个工具的 Watch 命令；
//   - 模块中有 .templ 文件时：go tool templ generate --watch，修改 .templ 后重新生成代码，在 ProxyPort 上代理应用并刷新浏览器；
//     监视 Static 目录，文件变化时刷新浏览器。
//
// 启动前检查所需的命令都已安装，并运行各工具的 Prepare。任何一个进程意外退出时其余的一起停止。
// 停止后运行 templ generate（有 .templ 文件时）和各工具的 Build，生成正式的代码、样式表和前端构建产物。
func New[C any](opts Options, addr func(C) string) *cobra.Command {
	if opts.Main == "" {
		panic("devcmd: Options.Main is required")
	}
	if opts.Serve == "" {
		opts.Serve = "serve"
	}
	if opts.ProxyPort == 0 {
		opts.ProxyPort = 7331
	}
	return &cobra.Command{
		Use:   "dev",
		Short: "开发模式：运行应用和前端工具，修改代码后重新编译、重启",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := command.LoadConfig[C](cmd)
			if err != nil {
				return err
			}
			configs, _ := cmd.Flags().GetStringSlice("config")
			target, err := proxyTarget(addr(cfg))
			if err != nil {
				return command.AsExitError(command.ExitConfig, err)
			}
			d := dev{opts: opts, configs: configs, target: target, templ: hasTempl("."),
				out: shared(cmd.OutOrStdout()), errOut: shared(cmd.ErrOrStderr())}
			if err := d.check(); err != nil {
				return command.AsExitError(command.ExitUnavailable, err)
			}
			for _, tool := range opts.Tools {
				if tool.Prepare != nil {
					if err := tool.Prepare(); err != nil {
						return command.AsExitError(command.ExitUnavailable, err)
					}
				}
			}
			if d.templ {
				fmt.Fprintf(d.out, "dev: 打开 http://localhost:%d（代理 %s）\n", opts.ProxyPort, target)
			} else {
				fmt.Fprintf(d.out, "dev: 打开 %s\n", target)
			}
			err = command.Concurrently(cmd.Context(), d.runs()...)
			// 停止后恢复正式的生成结果：templ 的 watch 模式生成的代码读取 _templ.txt，工具的输出也没有压缩。
			return errors.Join(err, d.finish())
		},
	}
}

type dev struct {
	opts        Options
	configs     []string
	target      string
	templ       bool // 模块中有 .templ 文件
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

// proxyTarget 把监听地址（例如 :8080、0.0.0.0:8080）转换为代理访问应用的地址。
func proxyTarget(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", errors.Wrapf(err, "devcmd: http address %q", addr)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "localhost"
	}
	return "http://" + net.JoinHostPort(host, port), nil
}

// check 确认 go、templ（有 .templ 文件时，go.mod 中的工具）和各工具的命令都已安装。
func (d dev) check() error {
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("devcmd: go not found in PATH")
	}
	if d.templ {
		if out, err := exec.Command("go", "tool", "-n", "templ").CombinedOutput(); err != nil {
			return errors.Newf("devcmd: templ is not a tool of this module, run: go get -tool github.com/a-h/templ/cmd/templ\n%s", out)
		}
	}
	for _, tool := range d.opts.Tools {
		if len(tool.Watch) == 0 {
			return errors.New("devcmd: Tool.Watch is empty")
		}
		for _, args := range [][]string{tool.Watch, tool.Build} {
			if len(args) == 0 {
				continue
			}
			if _, err := exec.LookPath(args[0]); err != nil {
				switch args[0] {
				case "tailwindcss":
					return errors.New("devcmd: tailwindcss not found in PATH, download the standalone CLI from https://github.com/tailwindlabs/tailwindcss/releases")
				case "bun":
					return errors.New("devcmd: bun not found in PATH, install it from https://bun.sh")
				}
				return errors.Newf("devcmd: %s not found in PATH", args[0])
			}
		}
	}
	return nil
}

// commands 返回与应用一起运行的监听命令。
func (d dev) commands() [][]string {
	var cmds [][]string
	if d.templ {
		cmds = append(cmds, []string{
			"go", "tool", "templ", "generate", "--watch",
			"--proxy=" + d.target,
			"--proxyport=" + strconv.Itoa(d.opts.ProxyPort),
			"--open-browser=false",
		})
	}
	for _, tool := range d.opts.Tools {
		cmds = append(cmds, tool.Watch)
	}
	return cmds
}

func (d dev) runs() []func(context.Context) error {
	runs := []func(context.Context) error{d.serve}
	for _, args := range d.commands() {
		runs = append(runs, func(ctx context.Context) error { return d.run(ctx, args) })
	}
	if d.templ && d.opts.Static != "" {
		runs = append(runs, func(ctx context.Context) error {
			return watch(ctx, func() string { return snapshot(d.opts.Static, nil) }, 250*time.Millisecond, d.reload)
		})
	}
	return runs
}

// run 运行一个监听进程，ctx 取消时向它的整个进程组发送中断信号。
func (d dev) run(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = d.out, d.errOut
	interruptGroup(cmd)
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return errors.Wrapf(err, "devcmd: %s", strings.Join(args, " "))
}

// reload 通知 templ 的代理刷新浏览器。
func (d dev) reload() {
	url := fmt.Sprintf("http://127.0.0.1:%d/_templ/reload/events", d.opts.ProxyPort)
	if res, err := http.Post(url, "", nil); err == nil {
		res.Body.Close()
	}
}

// finish 生成正式的 templ 代码，再运行各工具的 Build。
func (d dev) finish() error {
	var steps [][]string
	if d.templ {
		steps = append(steps, []string{"go", "tool", "templ", "generate"})
	}
	for _, tool := range d.opts.Tools {
		if len(tool.Build) > 0 {
			steps = append(steps, tool.Build)
		}
	}
	for _, args := range steps {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Stdout, cmd.Stderr = d.out, d.errOut
		if err := cmd.Run(); err != nil {
			return errors.Wrapf(err, "devcmd: %s", strings.Join(args, " "))
		}
	}
	return nil
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
			if path != dir && skipDir(d.Name()) {
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

// skipDir 报告是否跳过这个目录：隐藏目录、依赖、测试数据和前端的构建产物。
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" || name == "testdata" || name == "dist"
}

// goSource 报告 path 是否影响应用的编译：.go（测试除外）、go.mod、go.sum。
func goSource(path string) bool {
	base := filepath.Base(path)
	return strings.HasSuffix(base, ".go") && !strings.HasSuffix(base, "_test.go") || base == "go.mod" || base == "go.sum"
}

// hasTempl 报告 dir 中是否有 .templ 文件。
func hasTempl(dir string) bool {
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case found:
			return fs.SkipAll
		case err != nil:
			return nil
		case d.IsDir() && path != dir && skipDir(d.Name()):
			return filepath.SkipDir
		case !d.IsDir() && strings.HasSuffix(path, ".templ"):
			found = true
		}
		return nil
	})
	return found
}
