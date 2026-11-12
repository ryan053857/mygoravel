package migratecmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/database/migrate"
)

// Publish 创建按需注册的 publish 子命令，把 sources 中的迁移复制到 --dir。
// 通过 New(...).AddCommand(Publish(...)) 挂载；不加载应用配置、不连接数据库。
func Publish(sources ...migrate.Source) *cobra.Command {
	var dir string
	cmd := leaf("publish", "把框架迁移发布到应用目录", func(cmd *cobra.Command) error {
		if err := cmd.Context().Err(); err != nil {
			return err
		}
		written, err := migrate.Publish(dir, sources...)
		for _, name := range written {
			if _, writeErr := fmt.Fprintln(cmd.OutOrStdout(), name); writeErr != nil {
				return writeErr
			}
		}
		return err
	})
	cmd.Flags().StringVar(&dir, "dir", "migrations", "应用的 SQL 迁移目录")
	return cmd
}
