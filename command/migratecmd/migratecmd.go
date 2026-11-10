// Package migratecmd 提供 migrate up/status/down 命令。
package migratecmd

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"text/tabwriter"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/pressly/goose/v3"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/database/migrate"
)

// New 构造 migrate 命令及其 up、status、down、make 子命令。fsys 的根目录直接包含 SQL 迁移文件，
// dbConfig 从应用配置中取出数据库配置。数据库连不上时以 ExitUnavailable 退出。
func New[C any](fsys fs.FS, dbConfig func(cfg C) database.Config) *cobra.Command {
	// withProvider 连接数据库并创建迁移器，run 结束后关闭连接。
	withProvider := func(run func(context.Context, io.Writer, *goose.Provider) error) func(*cobra.Command) error {
		return func(cmd *cobra.Command) error {
			cfg, err := command.LoadConfig[C](cmd)
			if err != nil {
				return err
			}
			db, err := database.Open(cmd.Context(), dbConfig(cfg))
			if err != nil {
				return command.AsExitError(command.ExitUnavailable, err)
			}
			defer db.Close()
			provider, err := migrate.NewProvider(db.DB, fsys)
			if err != nil {
				return err
			}
			return run(cmd.Context(), cmd.OutOrStdout(), provider)
		}
	}
	root := leaf("migrate", "数据库迁移", func(cmd *cobra.Command) error { return cmd.Help() })
	root.AddCommand(
		leaf("up", "执行全部未执行的迁移", withProvider(func(ctx context.Context, out io.Writer, p *goose.Provider) error {
			results, err := p.Up(ctx)
			printResults(out, results)
			return errors.Wrap(err, "migrate: up")
		})),
		leaf("status", "查看迁移状态", withProvider(func(ctx context.Context, out io.Writer, p *goose.Provider) error {
			statuses, err := p.Status(ctx)
			if err != nil {
				return errors.Wrap(err, "migrate: status")
			}
			tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "VERSION\tSTATE\tAPPLIED AT\tFILE")
			for _, s := range statuses {
				applied := "-"
				if !s.AppliedAt.IsZero() {
					applied = s.AppliedAt.Local().Format(time.DateTime)
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\n", s.Source.Version, s.State, applied, path.Base(s.Source.Path))
			}
			return tw.Flush()
		})),
		leaf("down", "回滚最近一次迁移", withProvider(func(ctx context.Context, out io.Writer, p *goose.Provider) error {
			result, err := p.Down(ctx)
			if result != nil {
				printResults(out, []*goose.MigrationResult{result})
			}
			return errors.Wrap(err, "migrate: down")
		})),
		Make(),
	)
	return root
}

// printResults 每行输出一个已完成的迁移，例如 "up    20261111090000_create_articles.sql  12ms"。
func printResults(out io.Writer, results []*goose.MigrationResult) {
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	for _, r := range results {
		if r.Error == nil {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Direction, path.Base(r.Source.Path), r.Duration.Round(time.Millisecond))
		}
	}
	tw.Flush()
}

// leaf 构造不接受位置参数的命令。
func leaf(use, short string, run func(cmd *cobra.Command) error) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  command.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return run(cmd) },
	}
}
