// Package schedulecmd 提供 schedules 命令：列出定时任务、下一次执行的时间和是否正在执行。
package schedulecmd

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/schedule"
)

// New 返回 schedules 命令，按注册顺序列出每个任务的名称、表达式、下一次执行的时间、距现在多久、
// 是否正在执行（在任何一个实例上）和说明。不登记执行状态的任务（EveryInstance、AllowOverlap）显示为 -，
// 不会再执行的任务的下一次执行时间显示为 -。
//
// tasks 返回注册了全部任务的调度器，命令执行时才调用，通常是应用中注册任务的方法，例如 a.tasks。
// 它和其他需要数据库的命令一样在 command.Bind 中声明，声明时 App 还没有装配，所以传入的是方法而不是调度器。
func New(tasks func() *schedule.Scheduler) *cobra.Command {
	return &cobra.Command{
		Use:   "schedules",
		Short: "列出定时任务",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			sched := tasks()
			running, err := sched.Running(cmd.Context())
			if err != nil {
				return err
			}
			now := time.Now()
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "TASK\tEXPRESSION\tNEXT RUN\tIN\tRUNNING\tDESCRIPTION")
			for _, t := range sched.Tasks() {
				next, in := "-", "-"
				if at := t.Next(now); !at.IsZero() {
					next, in = at.Format(time.DateTime), at.Sub(now).Round(time.Second).String()
				}
				state := "-"
				switch r, ok := running[t.Name]; {
				case ok && r:
					state = "yes"
				case ok:
					state = "no"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.Expr, next, in, state, t.Description)
			}
			return w.Flush()
		},
	}
}
