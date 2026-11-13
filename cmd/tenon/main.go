// Command tenon 是 Tenon 应用的开发工具：init 生成新项目，dev 运行应用和前端工具，build 构建前端和二进制，
// generate 生成客户端。
// 它读取项目根目录的 tenon.json（可以没有，全部用约定的默认值）。应用把它加入 go.mod 的工具，版本随框架固定：
//
//	go get -tool github.com/go-tenon/tenon/cmd/tenon
//	go tool tenon dev
//
// 前端开发者在 package.json 中写 "dev": "go tool tenon dev"，用 bun run dev 开始。
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"path/filepath"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/go-tenon/tenon/cmd/tenon/internal/dev"
	"github.com/go-tenon/tenon/cmd/tenon/internal/project"
	"github.com/go-tenon/tenon/cmd/tenon/internal/scaffold"
)

const framework = "github.com/go-tenon/tenon"

func initCommand() *cobra.Command {
	var opts scaffold.Options
	cmd := &cobra.Command{
		Use:   "init <dir>",
		Short: "生成新项目：Go 应用加 React 单页应用，不依赖任何外部服务",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := args[0]
			base := filepath.Base(dir)
			if opts.Module == "" {
				opts.Module = base
			}
			if opts.Name == "" {
				opts.Name = base
			}
			version, local := tenonVersion()
			if opts.TenonDir == "" {
				opts.TenonDir = local
			}
			if opts.TenonDir != "" {
				abs, err := filepath.Abs(opts.TenonDir)
				if err != nil {
					return err
				}
				opts.TenonDir, version = abs, "v0.0.0"
			}
			if version == "" || version == "(devel)" {
				return fmt.Errorf("cannot tell which version of %s to use; run go run %s/cmd/tenon@latest init, or pass --tenon <checkout>", framework, framework)
			}
			opts.TenonVersion = version
			if err := scaffold.Write(dir, opts); err != nil {
				return err
			}
			// 生成客户端要编译应用，嵌入的 dist 要先存在。
			if err := dev.EnsureDist(&project.Config{Frontend: &project.Frontend{Dir: filepath.Join(dir, "frontend")}}); err != nil {
				return err
			}
			steps := [][]string{{"go", "mod", "tidy"}, {"go", "run", "./cmd/api", "client", "--ts", "frontend/src/api"}}
			_, err := exec.LookPath("bun")
			hasBun := err == nil
			if hasBun {
				steps = append(steps, []string{"bun", "install"})
			}
			for _, s := range steps {
				if err := run(cmd, dir, s[0], s[1:]...); err != nil {
					return err
				}
			}
			next := "  cd " + dir + "\n"
			if !hasBun {
				next += "  # 先安装 Bun：https://bun.sh\n  bun install\n"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "\n已生成 %s。接着运行：\n\n%s  bun run dev\n\n然后打开 http://localhost:8080。\n", dir, next)
			return nil
		},
	}
	cmd.Flags().StringVar(&opts.Module, "module", "", "Go 模块路径，默认是目录名")
	cmd.Flags().StringVar(&opts.Name, "name", "", "应用的名称，默认是目录名")
	cmd.Flags().StringVar(&opts.TenonDir, "tenon", "", "本地的框架目录，go.mod 用 replace 指向它（开发框架本身时使用）")
	return cmd
}

// tenonVersion 返回编译这个程序时框架的版本；框架被 replace 到本地目录时还返回这个目录。
func tenonVersion() (version, dir string) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", ""
	}
	for _, m := range append([]*debug.Module{&info.Main}, info.Deps...) {
		if m.Path != framework {
			continue
		}
		if m.Replace != nil && m.Replace.Version == "" {
			return m.Version, m.Replace.Path
		}
		return m.Version, ""
	}
	return "", ""
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := root().ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tenon:", err)
		os.Exit(1)
	}
}

func root() *cobra.Command {
	root := &cobra.Command{
		Use:           "tenon",
		Short:         "Tenon 应用的开发工具",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(initCommand(), devCommand(), buildCommand(), generateCommand())
	return root
}

func load() (*project.Config, error) { return project.Load(".") }

func devCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "dev",
		Short: "运行应用和前端工具，修改代码后重新编译、重启并生成客户端",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			return dev.Run(cmd.Context(), cfg, cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
}

func buildCommand() *cobra.Command {
	var out string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "构建前端，再编译应用（嵌入前端的构建产物）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			if cfg.Frontend != nil {
				root := dev.FrontendRoot(cfg)
				if err := run(cmd, root, "bun", "install", "--frozen-lockfile"); err != nil {
					return err
				}
				if err := run(cmd, root, "bun", "x", "vite", "build"); err != nil {
					return err
				}
			}
			if out == "" {
				out = filepath.Join("bin", path.Base(cfg.Main))
			}
			return run(cmd, ".", "go", "build", "-trimpath", "-o", out, cfg.Main)
		},
	}
	cmd.Flags().StringVarP(&out, "output", "o", "", "输出的二进制，默认 bin/<入口包的目录名>")
	return cmd
}

func generateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "generate",
		Short: "编译应用，按 tenon.json 生成 TypeScript 和 Go 客户端",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := load()
			if err != nil {
				return err
			}
			args := dev.GenerateArgs(cfg)
			if args == nil {
				return fmt.Errorf("nothing to generate: set generate.ts or generate.go in %s", project.File)
			}
			tmp, err := os.MkdirTemp("", "tenon-")
			if err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(tmp) }()
			if err := dev.EnsureDist(cfg); err != nil {
				return err
			}
			bin := filepath.Join(tmp, "app")
			if err := run(cmd, ".", "go", "build", "-o", bin, cfg.Main); err != nil {
				return err
			}
			return run(cmd, ".", bin, args...)
		},
	}
}

func run(cmd *cobra.Command, dir, name string, args ...string) error {
	c := exec.CommandContext(cmd.Context(), name, args...)
	c.Dir = dir
	c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("%s %v: %w", name, args, err)
	}
	return nil
}
