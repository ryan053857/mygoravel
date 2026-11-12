package migratecmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// Make 创建 make 子命令，在 --dir 中按命名约定新建迁移文件，例如
// "migrate make create_articles_table"。New 已经包含它；在还没有迁移、应用的嵌入包无法编译时，
// 可以把它挂到独立的入口上。不加载配置、不连接数据库。
func Make() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "make <name>",
		Short: "新建迁移文件，例如 make create_articles_table",
		Args: func(cmd *cobra.Command, args []string) error {
			return command.AsExitError(command.ExitUsage, cobra.ExactArgs(1)(cmd, args))
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := migrate.Create(dir, args[0])
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
