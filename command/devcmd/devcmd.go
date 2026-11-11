// Package devcmd 提供 dev 命令：开发服务端渲染页面时，一条命令运行全部监听进程，浏览器自动刷新。
//
//	root.AddCommand(devcmd.New(devcmd.Options{
//		Main:   "./cmd/api",
//		Static: "internal/web/static",
//		Tools: []devcmd.Tool{
//			devcmd.Tailwind("internal/web/app.css", "internal/web/static/css/app.css"),
//		},
//	}, func(cfg settings.Config) string { return cfg.HTTP.Addr }))
//
// 只使用 web/ui 预编译样式表的应用不需要 Tailwind，Tools 可以为空。
//
// 然后运行 go run ./cmd/api dev --config config/app.yaml,config/local.yaml，打开 http://localhost:7331。
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

	"github.com/ryan053857/mygoravel/command"
)

// Options 配置 dev 命令。
type Options struct {
	// Main 是应用入口的包路径，例如 ./cmd/api。必填。
	Main string
	// Serve 是启动 HTTP 服务的子命令，默认 serve。
	Serve string
	// ProxyPort 是浏览器访问的端口，默认 7331。templ 在这里代理应用，并注入自动刷新的脚本。
	ProxyPort int
	// Static 是静态文件目录，例如 internal/web/static。其中的文件变化时刷新浏览器，
	// 例如 Tailwind 重新生成了样式表。为空时不监视。
	Static string
	// Tools 是与 templ 一起运行的生成工具，例如 Tailwind、组件脚本的打包。
	Tools []Tool
}

// Tool 是一个生成工具：开发时运行 Watch 监听源文件，dev 停止后运行一次 Build。
// 监听模式的输出通常没有压缩，Build 把它恢复为可以提交的正式版本。
//
//	devcmd.Tool{Watch: []string{"esbuild", "js/app.js", "--outdir=static/js", "--watch"}, Build: []string{"esbuild", "js/app.js", "--outdir=static/js", "--minify"}}
type Tool struct {
	Watch []string
	Build []string // 可空
	// Prepare 在启动 Watch 之前运行一次，可空。
	Prepare func() error
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

const framework = "github.com/ryan053857/mygoravel"

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
//   - go tool templ generate --watch：修改 .templ 或 .go 后重新生成代码、重启应用（go run Main Serve，
//     带上相同的 --config），并通过 ProxyPort 上的代理刷新浏览器；
//   - Tools 中每个工具的 Watch 命令；
//   - 监视 Static 目录，文件变化时通知代理刷新浏览器。
//
// 启动前检查所需的命令都已安装。任何一个进程退出时其余的一起停止。
// 停止后运行 templ generate 和各工具的 Build，生成正式的代码、样式表和脚本，可以直接提交。
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
		Short: "开发模式：监听文件变化，重新生成、重启应用并刷新浏览器",
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
			d := dev{opts: opts, configs: configs, target: target, out: shared(cmd.OutOrStdout()), errOut: shared(cmd.ErrOrStderr())}
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
			fmt.Fprintf(d.out, "dev: 打开 http://localhost:%d（代理 %s）\n", opts.ProxyPort, target)
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

// check 确认 go、templ（go.mod 中的工具）和各工具的命令都已安装。
func (d dev) check() error {
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("devcmd: go not found in PATH")
	}
	if out, err := exec.Command("go", "tool", "-n", "templ").CombinedOutput(); err != nil {
		return errors.Newf("devcmd: templ is not a tool of this module, run: go get -tool github.com/a-h/templ/cmd/templ\n%s", out)
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
				if args[0] == "tailwindcss" {
					return errors.New("devcmd: tailwindcss not found in PATH, download the standalone CLI from https://github.com/tailwindlabs/tailwindcss/releases")
				}
				return errors.Newf("devcmd: %s not found in PATH", args[0])
			}
		}
	}
	return nil
}

// serveCommand 是 templ 重启应用时执行的 shell 命令。
func (d dev) serveCommand() string {
	args := []string{"go", "run", d.opts.Main, d.opts.Serve}
	for _, c := range d.configs {
		args = append(args, "--config="+c)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

// shellQuote 按 POSIX shell 的规则引用 s：templ 用 $SHELL -c 执行 --cmd。
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_./=,:@+", r))
	}) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// commands 返回要同时运行的命令。
func (d dev) commands() [][]string {
	cmds := [][]string{{
		"go", "tool", "templ", "generate", "--watch",
		"--proxy=" + d.target,
		"--proxyport=" + strconv.Itoa(d.opts.ProxyPort),
		"--open-browser=false",
		"--cmd=" + d.serveCommand(),
	}}
	for _, tool := range d.opts.Tools {
		cmds = append(cmds, tool.Watch)
	}
	return cmds
}

func (d dev) runs() []func(context.Context) error {
	var runs []func(context.Context) error
	for _, args := range d.commands() {
		runs = append(runs, func(ctx context.Context) error { return d.run(ctx, args) })
	}
	if d.opts.Static != "" {
		runs = append(runs, func(ctx context.Context) error {
			return watch(ctx, d.opts.Static, 250*time.Millisecond, d.reload)
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
	steps := [][]string{{"go", "tool", "templ", "generate"}}
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

// watch 每隔 every 检查一次 dir 中文件的修改时间和大小，有变化时调用 changed。
// 目录不存在时等待它出现。
func watch(ctx context.Context, dir string, every time.Duration, changed func()) error {
	last := snapshot(dir)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if cur := snapshot(dir); cur != last {
				last = cur
				changed()
			}
		}
	}
}

func snapshot(dir string) string {
	var b strings.Builder
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			fmt.Fprintf(&b, "%s %d %d\n", path, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return b.String()
}
