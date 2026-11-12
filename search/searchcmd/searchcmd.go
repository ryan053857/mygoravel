// Package searchcmd 提供 search reindex 命令：发起全文搜索索引或向量索引的重建，显示进度直到完成。
package searchcmd

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/command"
	"github.com/ryan053857/mygoravel/database"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/operation"
)

// Index 是可以重建的索引，*search.Index[D] 和 *vector.Index[D] 都实现了它。
type Index interface {
	Name() string
	Reindex(ctx context.Context, tx bun.IDB, ops *operation.Service, owner identity.Subject) (*operation.Operation, error)
}

// owner 是命令发起的重建所属的主体。
var owner = identity.Subject{Type: identity.System, ID: "cli"}

// pollInterval 是查看进度的间隔。
var pollInterval = time.Second

// New 返回 search 命令，db 是应用的数据库，indexes 是可以重建的索引，可以同时有全文搜索索引和向量索引，
// 名称不能重复：
//
//	search reindex <index>            发起重建，由 worker 执行，每秒显示一次进度，直到完成
//	search reindex <index> --detach   发起之后立即返回
//
// 显示进度时按 Ctrl-C 只停止查看，重建在 worker 中继续。重建失败时命令以非 0 状态退出。
// 和其他需要数据库的命令一样，在 command.Bind 中声明，db 在执行时装配。索引的名称重复时 panic。
func New(db *bun.DB, indexes ...Index) *cobra.Command {
	seen := map[string]bool{}
	for _, ix := range indexes {
		if seen[ix.Name()] {
			panic(errors.Newf("searchcmd: duplicate index name %q", ix.Name()))
		}
		seen[ix.Name()] = true
	}
	reindex := &cobra.Command{
		Use:   "reindex <index>",
		Short: "重建全文搜索索引或向量索引",
		Args:  command.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			i := slices.IndexFunc(indexes, func(ix Index) bool { return ix.Name() == args[0] })
			if i < 0 {
				names := make([]string, len(indexes))
				for i, ix := range indexes {
					names[i] = ix.Name()
				}
				return command.AsExitError(command.ExitUsage,
					errors.Newf("search: no index %q, available: %s", args[0], strings.Join(names, ", ")))
			}
			// 从 cmd 读取选项：command.Bind 用重新声明的命令的 RunE 执行，选项只解析在 cmd 上。
			detach, _ := cmd.Flags().GetBool("detach")
			return run(cmd, db, indexes[i], detach)
		},
	}
	reindex.Flags().Bool("detach", false, "发起重建后立即返回，不等待完成")
	root := &cobra.Command{Use: "search", Short: "全文搜索索引和向量索引"}
	root.AddCommand(reindex)
	return root
}

func run(cmd *cobra.Command, db *bun.DB, ix Index, detach bool) error {
	ctx, out := cmd.Context(), cmd.OutOrStdout()
	ops := operation.New(db)
	var op *operation.Operation
	err := database.RunInTx(ctx, db, nil, func(ctx context.Context, tx database.Tx) error {
		var err error
		op, err = ix.Reindex(ctx, tx, ops, owner)
		return err
	})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "已发起 %s 的重建，操作 %s，由 worker 执行\n", ix.Name(), op.ID)
	if detach {
		return nil
	}

	last := ""
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(out, "已停止查看，重建在 worker 中继续")
			return nil
		case <-time.After(pollInterval):
		}
		op, err = ops.Get(ctx, owner, op.ID)
		if err != nil {
			if ctx.Err() != nil {
				continue // 下一轮报告停止查看
			}
			return err
		}
		switch op.Status {
		case operation.StatusCompleted:
			fmt.Fprintf(out, "重建完成：%d 个 key，其中 %d 个无法建索引\n", op.Processed, op.Failed)
			return nil
		case operation.StatusFailed:
			return command.AsExitError(command.ExitFailure,
				errors.Newf("search: reindex %s failed: %s", ix.Name(), op.ErrorMessage(ctx)))
		}
		line := "等待 worker 领取任务……"
		if op.Status == operation.StatusRunning {
			line = fmt.Sprintf("已处理 %d / 约 %d 个 key", op.Processed, max(op.Total, op.Processed))
		}
		if line != last {
			fmt.Fprintln(out, line)
			last = line
		}
	}
}
