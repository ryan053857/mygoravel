// Package cachecmd 提供 cache stats 命令：查看 cachemon.Wrap 记录的缓存命中率。
package cachecmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/monitor/cachemon"
)

// New 返回 cache 命令：
//
//	cache stats [--since 1h]  每个缓存最近一段时间的命中、未命中、出错次数、命中率和读取的平均耗时
//
// m 是 cachemon.Wrap 记录运行统计的 Monitor。和其他需要数据库的命令一样在 command.Bind 中声明：
// New 只保存参数，命令执行时才使用。
func New(m *monitor.Monitor) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "查看缓存的命中率",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			since, _ := cmd.Flags().GetDuration("since")
			if since <= 0 {
				return command.AsExitError(command.ExitUsage, errors.New("cachecmd: --since must be positive"))
			}
			now := time.Now()
			caches, err := cachemon.Activity(cmd.Context(), m, now.Add(-since), now)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "最近 %s\n", shortDuration(since))
			w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "CACHE\tHITS\tMISSES\tERRORS\tHIT RATE\tAVG GET")
			for _, name := range slices.Sorted(maps.Keys(caches)) {
				a := caches[name]
				rate, get := "-", "-"
				if gets := a.Gets(); gets.Count > 0 {
					rate = fmt.Sprintf("%.1f%%", a.HitRate()*100)
					get = gets.MeanDuration().Round(time.Microsecond).String()
				}
				fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s\n", name, a.Hits.Count, a.Misses.Count, a.Errors, rate, get)
			}
			return w.Flush()
		},
	}
	cmd.Flags().Duration("since", time.Hour, "统计最近多长时间")
	root := &cobra.Command{Use: "cache", Short: "缓存"}
	root.AddCommand(cmd)
	return root
}

// shortDuration 去掉时长末尾为 0 的分和秒，例如 24h0m0s 显示为 24h。
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
