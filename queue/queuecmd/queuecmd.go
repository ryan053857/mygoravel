// Package queuecmd 提供 queue failed 命令：列出失败任务。队列中积压的任务数由 nsqd 统计，在 nsqadmin 中查看。
package queuecmd

import (
	"fmt"
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

// New 返回 queue 命令。failed 是失败任务表：
//
//	queue failed [--topic t] [--limit n] 从新到旧列出失败任务
//
// 和其他需要数据库的命令一样，在 command.Bind 中声明：New 只保存参数，命令执行时才使用它。
func New(failed *queue.FailedJobs) *cobra.Command {
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
			fmt.Fprintln(w, "ID\tTOPIC\tATTEMPTS\tFAILED AT\tERROR")
			for _, j := range jobs {
				fmt.Fprintln(w, strings.Join([]string{j.ID.String(), j.Topic, strconv.Itoa(j.Attempts),
					j.FailedAt.Local().Format(time.DateTime), firstLine(j.Error)}, "\t"))
			}
			return w.Flush()
		},
	}
	list.Flags().String("topic", "", "只列出这个 topic 的失败任务")
	list.Flags().Int("limit", 20, "最多列出的条数")

	root := &cobra.Command{Use: "queue", Short: "队列"}
	root.AddCommand(list)
	return root
}

// firstLine 返回错误信息的第一行，超过 errorWidth 个字符时截断。
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if utf8.RuneCountInString(s) <= errorWidth {
		return s
	}
	return string([]rune(s)[:errorWidth-1]) + "…"
}
