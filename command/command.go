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

	"github.com/go-tenon/tenon/config"
)

const configFlag = "config"

// Root 返回应用的根命令，用 AddCommand 装配需要的命令。
// 根命令定义了所有子命令共用的 --config 选项：可以重复传入多个配置文件，
// 后面的覆盖前面的，默认为 config/app.yaml。未知命令、未知选项和多余参数标记为 ExitUsage。
func Root(name, short string) *cobra.Command {
	root := &cobra.Command{
		Use:           name,
		Short:         short,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          NoArgs,
		RunE:          func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error { return AsExitError(ExitUsage, err) })
	root.PersistentFlags().StringSlice(configFlag, []string{"config/app.yaml"}, "配置文件，可以传入多次，后面的覆盖前面的")
	return root
}

// Run 构造一个加载配置后执行 run 的命令，适合 serve、worker 这类常驻进程，
// 也适合一次性任务。run 的 ctx 在进程收到 SIGINT、SIGTERM 时被取消。
func Run[C any](use, short string, run func(ctx context.Context, cfg C) error) *cobra.Command {
	return leaf(use, short, func(cmd *cobra.Command) error {
		cfg, err := LoadConfig[C](cmd)
		if err != nil {
			return err
		}
		return run(cmd.Context(), cfg)
	})
}

// LoadConfig 加载并校验 --config 指定的配置文件，失败时错误带 ExitConfig 退出码。
// 加载前先读取工作目录下的 .env（不存在时忽略），其中的变量不会覆盖进程中已经设置的变量。
// cmd 必须挂在 Root 返回的根命令下。
func LoadConfig[C any](cmd *cobra.Command) (C, error) {
	var zero C
	files, err := cmd.Flags().GetStringSlice(configFlag)
	if err != nil {
		return zero, errors.Wrap(err, "command: --config is defined by command.Root")
	}
	if err := config.LoadEnv(".env"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return zero, AsExitError(ExitConfig, err)
	}
	cfg, err := config.Load[C](files...)
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
		Args:  NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return run(cmd) },
	}
}

// group 构造只包含子命令的命令；直接执行时输出帮助。
func group(use, short string, children ...*cobra.Command) *cobra.Command {
	cmd := leaf(use, short, func(cmd *cobra.Command) error { return cmd.Help() })
	cmd.AddCommand(children...)
	return cmd
}

// NoArgs 是拒绝位置参数的 cobra.PositionalArgs，出错时带 ExitUsage 退出码。
// 自定义命令用它代替 cobra.NoArgs，多余参数就会以退出码 2 失败。
func NoArgs(cmd *cobra.Command, args []string) error {
	return AsExitError(ExitUsage, cobra.NoArgs(cmd, args))
}

// ExactArgs 是要求恰好 n 个位置参数的 cobra.PositionalArgs，出错时带 ExitUsage 退出码。
func ExactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		return AsExitError(ExitUsage, cobra.ExactArgs(n)(cmd, args))
	}
}
