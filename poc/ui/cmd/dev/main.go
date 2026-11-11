// dev 是开发命令的原型：用 command.Concurrently 同时运行三个 watch 进程，任何一个退出则全部停止。
//
//   - tailwindcss --watch：扫描 .templ 中的 class，重新生成 assets/css/app.css；
//   - shadcn-templ bundle --watch：组件 JS 变化时重新打包；
//   - templ generate --watch --proxy：重新生成 Go 代码、重启应用，并通过代理让浏览器自动刷新。
//
// 用法：go run ./cmd/dev，打开 http://localhost:7331（templ 的代理端口）。
package main

import (
	"context"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/ryan053857/mygoravel/command"
)

func run(name string, args ...string) func(context.Context) error {
	return func(ctx context.Context) error {
		cmd := exec.CommandContext(ctx, name, args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		cmd.Env = append(os.Environ(), "DEV=1")
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		return cmd.Run()
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := command.Concurrently(ctx,
		run("tailwindcss", "-i", "assets/css/globals.css", "-o", "assets/css/app.css", "--watch=always"),
		run("shadcn-templ", "bundle", "--watch"),
		run("go", "tool", "templ", "generate", "--watch", "--proxy=http://localhost:8080", "--cmd=go run ./cmd/demo", "--open-browser=false"),
	)
	if err != nil && ctx.Err() == nil {
		log.Fatal(err)
	}
}
