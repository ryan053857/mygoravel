package clientgen

import (
	"fmt"
	"runtime/debug"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/httpx/rest"
)

// Command 构造 client 命令：以文档模式创建 API、调用 register 注册路由，在 --out 目录生成 Go 客户端，
// 在 --ts 目录生成 TypeScript 客户端（见 GenerateTS），两者至少给出一个。
// 不读取配置，不调用处理器；需要在应用源码目录中运行（例如 go run ./cmd/api client --out ../api），
// 因为类型声明和错误码从源码复制。
//
// --main 默认是当前程序的主包；--import 允许客户端直接导入的包，可重复；--rename 指定镜像包名，例如
// --rename example.com/blog/internal/session=usersession。没有生成的端点及原因输出到标准错误。
func Command(opts rest.Options, register func(api *rest.API)) *cobra.Command {
	var gen Options
	var ts string
	cmd := &cobra.Command{
		Use:   "client",
		Short: "生成 Go 或 TypeScript 客户端",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if gen.Out == "" && ts == "" {
				return command.AsExitError(command.ExitUsage, errors.New("--out or --ts is required"))
			}
			if gen.Main == "" {
				if info, ok := debug.ReadBuildInfo(); ok {
					gen.Main = info.Path
				}
			}
			if gen.Main == "" {
				return command.AsExitError(command.ExitUsage, errors.New("cannot tell the main package; pass --main"))
			}
			opts.Mode = rest.ModeOpenAPI
			api := rest.New(opts)
			register(api)
			if ts != "" {
				res, err := GenerateTS(api, TSOptions{Main: gen.Main, Dir: gen.Dir, Rename: gen.Rename})
				if err != nil {
					return err
				}
				for _, s := range res.Skipped {
					fmt.Fprintln(cmd.ErrOrStderr(), "skipped "+s)
				}
				if err := Write(ts, res.Files); err != nil {
					return err
				}
			}
			if gen.Out == "" {
				return nil
			}
			res, err := Generate(api, gen)
			if err != nil {
				return err
			}
			for _, s := range res.Skipped {
				fmt.Fprintln(cmd.ErrOrStderr(), "skipped "+s)
			}
			return Write(gen.Out, res.Files)
		},
	}
	cmd.Flags().StringVar(&gen.Out, "out", "", "Go 客户端的输出目录，目录名是根包的包名，例如 ../api")
	cmd.Flags().StringVar(&ts, "ts", "", "TypeScript 客户端的输出目录，例如 frontend/src/api")
	cmd.Flags().StringVar(&gen.Main, "main", "", "应用的主包，在它的依赖中查找错误码；默认是当前程序")
	cmd.Flags().StringSliceVar(&gen.Imports, "import", nil, "客户端可以直接导入的包（含子包），可重复")
	cmd.Flags().StringToStringVar(&gen.Rename, "rename", nil, "服务端包路径=镜像包名，可重复")
	return cmd
}
