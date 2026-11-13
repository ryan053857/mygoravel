// Package queuecmd 提供 queue 命令：查看队列在数据库一侧的状态、列出失败任务、重建转发任务的复制槽。
// NSQ 中积压的任务数由 nsqd 统计，在 nsqadmin 中查看。
package queuecmd

import (
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/uptrace/bun"

	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/queue"
)

// errorWidth 是 queue failed 显示的错误信息的最大长度（字符）。
const errorWidth = 80

// New 返回 queue 命令。db 是发布任务的数据库：
//
//	queue status                         复制槽是否有 worker 在转发、积压多少，queue_scheduled 中的任务
//	queue failed [--topic t] [--limit n] 从新到旧列出失败任务
//	queue slot recreate [--force]        复制槽丢失或失效后重建它
//
// 和其他需要数据库的命令一样，在 command.Bind 中声明：New 只保存参数，命令执行时才使用它。
func New(db bun.IDB) *cobra.Command {
	status := &cobra.Command{
		Use:   "status",
		Short: "显示转发任务的复制槽和 queue_scheduled 的状态",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := queue.ReadStatus(cmd.Context(), db)
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "slot\t%s\n", s.Slot)
			switch {
			case !s.Exists:
				fmt.Fprintln(w, "state\tmissing: run the migrations, or `queue slot recreate` after a restore or failover")
			case s.WALStatus == "lost":
				fmt.Fprintln(w, "state\tlost: jobs in it are gone, run `queue slot recreate`")
			case s.Active:
				fmt.Fprintf(w, "state\trelaying (pid %d)\n", s.ActivePID)
			default:
				fmt.Fprintln(w, "state\tidle: no worker is relaying, the WAL keeps growing")
			}
			if s.Exists {
				fmt.Fprintf(w, "wal status\t%s\n", s.WALStatus)
				fmt.Fprintf(w, "lag\t%s\n", bytes(s.Lag))
			}
			fmt.Fprintf(w, "scheduled\t%d\n", s.Scheduled)
			if !s.NextScheduled.IsZero() {
				fmt.Fprintf(w, "next scheduled\t%s\n", s.NextScheduled.Local().Format(time.DateTime))
			}
			return w.Flush()
		},
	}

	recreate := &cobra.Command{
		Use:   "recreate",
		Short: "重建丢失或失效的复制槽",
		Long: "复制槽丢失（恢复备份、没有同步的主备切换）或失效后，worker 拒绝启动，用这个命令重建。\n" +
			"从丢失到重建期间发布的任务不会被转发，需要从业务数据中找出没有完成的工作重新发布。\n" +
			"复制槽可用时不重建，除非指定 --force：那会丢弃其中还没转发的任务。",
		Args: command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			force, _ := cmd.Flags().GetBool("force")
			if err := queue.RecreateSlot(cmd.Context(), db, force); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "replication slot recreated")
			return err
		},
	}
	recreate.Flags().Bool("force", false, "复制槽可用时也重建，丢弃其中还没转发的任务")
	slot := &cobra.Command{Use: "slot", Short: "转发任务的复制槽"}
	slot.AddCommand(recreate)

	list := &cobra.Command{
		Use:   "failed",
		Short: "列出失败任务，从新到旧",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// 从 cmd 读取选项：command.Bind 用重新声明的命令的 RunE 执行，选项只解析在 cmd 上。
			topic, _ := cmd.Flags().GetString("topic")
			limit, _ := cmd.Flags().GetInt("limit")
			jobs, err := queue.NewFailedJobs(db).List(cmd.Context(), queue.FailedQuery{Topic: topic, Limit: limit})
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
	root.AddCommand(status, list, slot)
	return root
}

// bytes 把字节数写成便于阅读的形式。
func bytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

// firstLine 返回错误信息的第一行，超过 errorWidth 个字符时截断。
func firstLine(s string) string {
	s, _, _ = strings.Cut(s, "\n")
	if utf8.RuneCountInString(s) <= errorWidth {
		return s
	}
	return string([]rune(s)[:errorWidth-1]) + "…"
}
