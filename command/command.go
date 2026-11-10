package command

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"syscall"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/config"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/httpx/rest"
)

// App 声明应用的命令行。C 是应用的根配置结构体。
// 字段为空时不安装对应的命令；config check/show 总是安装。
type App[C any] struct {
	// Name 是可执行文件名，Short 是帮助中的一句话描述。
	Name, Short string
	// ConfigFile 是 --config 的默认值，空值为 config.yaml。
	ConfigFile string

	// API 是 openapi、routes 命令创建 API 时使用的选项，Mode 固定为 rest.ModeOpenAPI。
	API rest.Options
	// Routes 注册应用的全部路由；设置后安装 openapi 和 routes。
	// 文档模式不调用处理器工厂，所以这里可以传入 nil 服务。
	Routes func(api *rest.API)

	// Serve 打开资源并启动 HTTP 服务，直到 ctx 被取消；设置后安装 serve。
	Serve func(ctx context.Context, cfg C) error

	// Migrations 是 SQL 迁移文件所在的 fs.FS，Database 从配置中取出数据库配置；
	// 两者都设置后安装 migrate up/status/down。
	Migrations fs.FS
	Database   func(cfg C) database.Config
}

// New 按 app 构造根命令。返回的是原生 *cobra.Command，可以继续 AddCommand。
func New[C any](app App[C]) *cobra.Command {
	root := &cobra.Command{
		Use:           app.Name,
		Short:         app.Short,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return AsExitError(ExitUsage, err) })
	file := app.ConfigFile
	if file == "" {
		file = "config.yaml"
	}
	root.PersistentFlags().String(configFlag, file, "配置文件路径")

	if app.Serve != nil {
		root.AddCommand(leaf("serve", "启动 HTTP 服务", func(cmd *cobra.Command) error {
			cfg, err := LoadConfig[C](cmd)
			if err != nil {
				return err
			}
			return app.Serve(cmd.Context(), cfg)
		}))
	}
	if app.Routes != nil {
		build := func() *rest.API {
			opts := app.API
			opts.Mode = rest.ModeOpenAPI
			api := rest.New(opts)
			app.Routes(api)
			return api
		}
		root.AddCommand(openAPICommand(build), routesCommand(build))
	}
	if app.Migrations != nil && app.Database != nil {
		root.AddCommand(migrateCommand(app.Migrations, func(cmd *cobra.Command) (database.Config, error) {
			cfg, err := LoadConfig[C](cmd)
			return app.Database(cfg), err
		}))
	}
	root.AddCommand(configCommand[C]())
	return root
}

const configFlag = "config"

// LoadConfig 按 --config 加载并校验配置。失败时错误带 ExitConfig 退出码。
// 自定义命令挂在 New 返回的根命令下时，可以用它读取配置。
func LoadConfig[C any](cmd *cobra.Command) (C, error) {
	path, err := cmd.Flags().GetString(configFlag)
	if err != nil {
		var zero C
		return zero, errors.Wrap(err, "command: --config flag is not defined")
	}
	cfg, err := config.Load[C](path)
	return cfg, AsExitError(ExitConfig, err)
}

// Execute 执行根命令：SIGINT、SIGTERM 会取消命令的 context；
// 失败时把错误写到标准错误，并以 ExitCode(err) 结束进程。
func Execute(root *cobra.Command) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := root.ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(root.ErrOrStderr(), err)
		os.Exit(ExitCode(err))
	}
}

// leaf 构造不接受位置参数的命令。
func leaf(use, short string, run func(cmd *cobra.Command) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  noArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return run(cmd) },
	}
}

// group 构造只包含子命令的命令；直接执行时输出帮助。
func group(use, short string, children ...*cobra.Command) *cobra.Command {
	cmd := leaf(use, short, func(cmd *cobra.Command) error { return cmd.Help() })
	cmd.AddCommand(children...)
	return cmd
}

func noArgs(cmd *cobra.Command, args []string) error {
	return AsExitError(ExitUsage, cobra.NoArgs(cmd, args))
}
