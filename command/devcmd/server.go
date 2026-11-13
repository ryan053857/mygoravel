package devcmd

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
)

// pollInterval 是检查 Go 源文件变化的间隔。
var pollInterval = 300 * time.Millisecond

// stopTimeout 是平滑停止应用的时限，超过后强制结束。
const stopTimeout = 10 * time.Second

// serve 编译并运行应用，Go 源文件变化后重新编译、重启，直到 ctx 取消。
// 编译失败只打印错误：已经在运行的应用继续运行，等下一次修改。
func (d dev) serve(ctx context.Context) error {
	dir, err := os.MkdirTemp("", "devcmd-")
	if err != nil {
		return errors.Wrap(err, "devcmd: temp dir")
	}
	defer func() { _ = os.RemoveAll(dir) }()
	bin := filepath.Join(dir, "app")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}

	var app *exec.Cmd
	exited := make(chan error, 1)
	defer func() { d.stop(app, exited) }()
	restart := func() {
		if !d.build(ctx, bin) {
			return
		}
		d.stop(app, exited)
		app, exited = d.start(bin)
		if app == nil {
			return
		}
		if d.templ {
			go d.reloadWhenUp(ctx)
		}
		d.generate(ctx, bin)
	}
	restart()

	changes := make(chan struct{}, 1)
	every := pollInterval
	watching := make(chan struct{})
	defer func() { <-watching }()
	go func() {
		defer close(watching)
		_ = watch(ctx, func() string { return snapshot(".", goSource) }, every, func() {
			select {
			case changes <- struct{}{}:
			default:
			}
		})
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			fmt.Fprintln(d.out, "dev: Go 代码有变化，重新编译")
			restart()
		case err := <-exited:
			// 应用自己退出（例如启动时配置错误）：等下一次修改后再启动。
			fmt.Fprintf(d.errOut, "dev: 应用已退出：%v，修改代码后重新启动\n", err)
			app = nil
		}
	}
}

// build 编译应用到 bin，失败时打印错误并返回 false。
func (d dev) build(ctx context.Context, bin string) bool {
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, d.opts.Main)
	cmd.Stdout, cmd.Stderr = d.out, d.errOut
	if err := cmd.Run(); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(d.errOut, "dev: 编译失败：%v\n", err)
		}
		return false
	}
	return true
}

// start 运行编译出的应用；exited 在它退出时收到结果。
func (d dev) start(bin string) (*exec.Cmd, chan error) {
	args := []string{d.opts.Serve}
	for _, c := range d.configs {
		args = append(args, "--config="+c)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = d.out, d.errOut
	cmd.Env = os.Environ()
	if d.templ {
		// 与 templ generate --watch 的 --cmd 相同：生成的代码从 _templ.txt 读取文本，只改文字时不必重新编译。
		root, _ := filepath.Abs(".")
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			root = resolved
		}
		cmd.Env = append(cmd.Env, "TEMPL_DEV_MODE=true", "TEMPL_DEV_MODE_WATCH_ROOT="+root)
	}
	ownGroup(cmd)
	exited := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(d.errOut, "dev: 启动应用失败：%v\n", err)
		return nil, exited
	}
	go func() { exited <- cmd.Wait() }()
	return cmd, exited
}

// stop 平滑停止应用：发送 SIGTERM，stopTimeout 之后强制结束。应用已经退出时什么也不做。
func (d dev) stop(app *exec.Cmd, exited chan error) {
	if app == nil {
		return
	}
	terminate(app)
	var err error
	select {
	case err = <-exited:
	case <-time.After(stopTimeout):
		kill(app)
		err = <-exited
	}
	_ = err // 停止是 dev 自己要求的，退出状态不重要
}

// generate 用编译出的程序运行 Options.Generate，失败时只打印错误。
func (d dev) generate(ctx context.Context, bin string) {
	if len(d.opts.Generate) == 0 {
		return
	}
	cmd := exec.CommandContext(ctx, bin, d.opts.Generate...)
	cmd.Stdout, cmd.Stderr = d.out, d.errOut
	if err := cmd.Run(); err != nil && ctx.Err() == nil {
		fmt.Fprintf(d.errOut, "dev: %s 失败：%v\n", strings.Join(d.opts.Generate, " "), err)
	}
}

// reloadWhenUp 等应用开始监听后刷新浏览器，最多等 10 秒。
func (d dev) reloadWhenUp(ctx context.Context) {
	host := strings.TrimPrefix(d.target, "http://")
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && ctx.Err() == nil; time.Sleep(100 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", host, time.Second); err == nil {
			_ = c.Close()
			d.reload()
			return
		}
	}
}
