// Command tenon 是 Tenon 应用的开发工具：dev 运行应用和前端工具，build 构建前端和二进制，generate 生成客户端。
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
	"syscall"

	"github.com/spf13/cobra"

	"github.com/go-tenon/tenon/cmd/tenon/internal/dev"
	"github.com/go-tenon/tenon/cmd/tenon/internal/project"
)

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
	root.AddCommand(devCommand(), buildCommand(), generateCommand())
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
