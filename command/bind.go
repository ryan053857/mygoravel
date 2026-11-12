package command

import (
	"context"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/cleanup"
)

// Bind 返回 declare 声明的命令，它们的依赖推迟到执行时才装配。
//
// 构造命令行时，declare 收到指向零值 D 的指针，只用来声明命令的名称、参数和选项，
// 所以 declare 和其中 action 的构造函数只能保存依赖、不能使用它们。
// 执行其中一个命令时，Bind 加载配置，调用 open 装配依赖，用装配好的 *D 再调用一次 declare，
// 然后执行同一路径上命令的 RunE。命令返回后，open 登记到 stack 的资源按相反顺序关闭。
//
// 选项解析在第一次声明的命令上，RunE 收到的 cmd 就是它，所以 RunE 用 cmd.Flags().GetString 这类方法读取选项，
// 不要用 StringVar 绑定的变量：重新声明时绑定的是另一组变量，它们不会被解析。
//
// 只有设置了 RunE 的命令会被替换，--help 等不执行 RunE 的调用不会装配依赖。
func Bind[C, D any](open func(ctx context.Context, cfg C, stack *cleanup.Stack) (*D, error), declare func(*D) []*cobra.Command) []*cobra.Command {
	cmds := declare(new(D))
	for _, cmd := range cmds {
		bind(cmd, nil, open, declare)
	}
	return cmds
}

func bind[C, D any](cmd *cobra.Command, parent []string, open func(context.Context, C, *cleanup.Stack) (*D, error), declare func(*D) []*cobra.Command) {
	path := append(parent[:len(parent):len(parent)], cmd.Name())
	if cmd.RunE != nil {
		cmd.RunE = func(cmd *cobra.Command, args []string) (err error) {
			cfg, err := LoadConfig[C](cmd)
			if err != nil {
				return err
			}
			var stack cleanup.Stack
			defer func() {
				err = stack.CloseWith(context.Background(), err)
			}()
			deps, err := open(cmd.Context(), cfg, &stack)
			if err != nil {
				return err
			}
			target := find(declare(deps), path)
			if target == nil || target.RunE == nil {
				return errors.Newf("command: %q is not declared with the assembled dependencies", strings.Join(path, " "))
			}
			return target.RunE(cmd, args)
		}
	}
	for _, child := range cmd.Commands() {
		bind(child, path, open, declare)
	}
}

// find 在 cmds 中按名称逐级查找 path 指向的命令。
func find(cmds []*cobra.Command, path []string) *cobra.Command {
	for _, cmd := range cmds {
		if cmd.Name() != path[0] {
			continue
		}
		if len(path) == 1 {
			return cmd
		}
		return find(cmd.Commands(), path[1:])
	}
	return nil
}
