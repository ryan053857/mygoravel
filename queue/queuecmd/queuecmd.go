// Package queuecmd 提供 queue stats 和 queue failed 命令：查看队列中的任务数和失败任务。
package queuecmd

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/queue"
)

// errorWidth 是 queue failed 显示的错误信息的最大长度（字符）。
const errorWidth = 80

// New 返回 queue 命令。failed 是失败任务表，drivers 是要统计的驱动：
//
//	queue stats                          每个 topic 当前各状态的任务数、积压最久的任务已经等了多久
//	queue failed [--topic t] [--limit n] 从新到旧列出失败任务
//
// 驱动给不出的数显示为 -。传入多个驱动时多一列驱动名。
// 和其他需要数据库的命令一样，在 command.Bind 中声明：New 只保存参数，命令执行时才使用它们。
// 没有传入驱动时 panic。
func New(failed *queue.FailedJobs, drivers ...queue.Driver) *cobra.Command {
	if len(drivers) == 0 {
		panic("queuecmd: no drivers")
	}
	multi := len(drivers) > 1
	current := &cobra.Command{
		Use:   "stats",
		Short: "查看队列中的任务数",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			header(w, multi, "TOPIC\tREADY\tDELAYED\tRETRYING\tRUNNING\tFAILED\tWAITING")
			for _, d := range drivers {
				stats, err := queue.Stats(cmd.Context(), d, failed)
				if err != nil {
					return err
				}
				for _, s := range stats {
					row(w, multi, d.Name(), s.Topic, count(s, queue.StateReady), count(s, queue.StateDelayed),
						count(s, queue.StateRetrying), count(s, queue.StateRunning), count(s, queue.StateFailed), waiting(s))
				}
			}
			return w.Flush()
		},
	}

	list := &cobra.Command{
		Use:   "failed",
		Short: "列出失败任务，从新到旧",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 从 cmd 读取选项：command.Bind 用重新声明的命令的 RunE 执行，选项只解析在 cmd 上。
			topic, _ := cmd.Flags().GetString("topic")
			limit, _ := cmd.Flags().GetInt("limit")
			jobs, err := failed.List(cmd.Context(), queue.FailedQuery{Topic: topic, Limit: limit})
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			header(w, multi, "ID\tTOPIC\tATTEMPTS\tFAILED AT\tERROR")
			for _, j := range jobs {
				row(w, multi, j.Driver, j.ID.String(), j.Topic, strconv.Itoa(j.Attempts),
					j.FailedAt.Local().Format(time.DateTime), firstLine(j.Error))
			}
			return w.Flush()
		},
	}
	list.Flags().String("topic", "", "只列出这个 topic 的失败任务")
	list.Flags().Int("limit", 20, "最多列出的条数")

	root := &cobra.Command{Use: "queue", Short: "队列"}
	root.AddCommand(current, list)
	return root
}

// header 输出表头，多个驱动时以 DRIVER 列开头。
func header(w io.Writer, multi bool, columns string) {
	if multi {
		columns = "DRIVER\t" + columns
	}
	fmt.Fprintln(w, columns)
}

// row 输出一行，多个驱动时以驱动名开头。
func row(w io.Writer, multi bool, driver string, cells ...string) {
	if multi {
		cells = append([]string{driver}, cells...)
	}
	fmt.Fprintln(w, strings.Join(cells, "\t"))
}

// count 返回一个状态的任务数，驱动给不出时为 -。
func count(s queue.TopicStats, state queue.State) string {
	n, ok := s.Jobs[state]
	if !ok {
		return "-"
	}
	return strconv.Itoa(n)
}

// waiting 返回积压最久的任务已经等了多久：没有积压时为 0s，驱动给不出时为 -。
func waiting(s queue.TopicStats) string {
	ready, ok := s.Jobs[queue.StateReady]
	switch {
	case ok && ready == 0:
		return "0s"
	case !ok || s.OldestReadyAt.IsZero():
		return "-"
	}
	return max(time.Since(s.OldestReadyAt), 0).Round(time.Second).String()
}

// firstLine 返回错误信息的第一行，超过 errorWidth 个字符时截断。
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if utf8.RuneCountInString(s) <= errorWidth {
		return s
	}
	return string([]rune(s)[:errorWidth-1]) + "…"
}
