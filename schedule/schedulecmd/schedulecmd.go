// Package schedulecmd 提供 schedules 命令：列出定时任务、下一次执行的时间、是否正在执行和上一次执行的结果。
package schedulecmd

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/monitor"
	"github.com/ryan053857/mygoravel/schedule"
)

// New 返回 schedules 命令，按注册顺序列出每个任务的名称、表达式、下一次执行的时间、距现在多久、
// 是否正在执行（在任何一个实例上）、上一次执行的时间、结果和耗时，以及说明。
// 不登记执行状态的任务（EveryInstance、AllowOverlap）的执行状态显示为 -，
// 不会再执行的任务的下一次执行时间显示为 -，没有执行记录的任务的上一次执行显示为 -。
//
// tasks 返回注册了全部任务的调度器，命令执行时才调用，通常是应用中注册任务的方法，例如 a.tasks。
// stats 是 schedule.WithMonitor 记录的运行统计，在执行时为 nil 表示没有记录，不显示上一次执行。
// 它和其他需要数据库的命令一样在 command.Bind 中声明，声明时 App 还没有装配，所以传入的是方法而不是调度器。
func New(tasks func() *schedule.Scheduler, stats *monitor.Store) *cobra.Command {
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
			var last map[string]schedule.Run
			if stats != nil {
				if last, err = schedule.LastRuns(cmd.Context(), stats); err != nil {
					return err
				}
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			columns := []string{"TASK", "EXPRESSION", "NEXT RUN", "IN", "RUNNING"}
			if stats != nil {
				columns = append(columns, "LAST RUN", "RESULT", "TOOK")
			}
			fmt.Fprintln(w, strings.Join(append(columns, "DESCRIPTION"), "\t"))
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
				cells := []string{t.Name, t.Expr, next, in, state}
				if stats != nil {
					cells = append(cells, lastRun(last[t.Name])...)
				}
				fmt.Fprintln(w, strings.Join(append(cells, t.Description), "\t"))
			}
			return w.Flush()
		},
	}
}

// lastRun 返回上一次执行的时间、结果和耗时。
func lastRun(r schedule.Run) []string {
	if r.Started.IsZero() {
		return []string{"-", "-", "-"}
	}
	result := "ok"
	if r.Failed() {
		result = "failed"
	}
	return []string{r.Started.Local().Format(time.DateTime), result, r.Duration.Round(time.Millisecond).String()}
}
