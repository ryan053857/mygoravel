package migratecmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// Make 创建 make 子命令，在 --dir 中按命名约定为 driver 新建迁移文件，例如
// "migrate make create_articles_table"。New 已经包含从应用配置读取 driver 的 make；
// 在还没有迁移、应用的嵌入包无法编译时，可以把 Make 挂到独立的入口上。不加载配置、不连接数据库。
func Make(driver database.Driver) *cobra.Command {
	return makeCmd(func(*cobra.Command) (database.Driver, error) { return driver, nil })
}

// makeCmd 构造 make 子命令，driver 在执行时取得。
func makeCmd(driver func(*cobra.Command) (database.Driver, error)) *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "make <name>",
		Short: "新建迁移文件，例如 make create_articles_table",
		Args: func(cmd *cobra.Command, args []string) error {
			return command.AsExitError(command.ExitUsage, cobra.ExactArgs(1)(cmd, args))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := driver(cmd)
			if err != nil {
				return err
			}
			path, err := migrate.Create(d, dir, args[0])
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), path)
			return err
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "migrations", "应用的 SQL 迁移目录")
	return cmd
}
