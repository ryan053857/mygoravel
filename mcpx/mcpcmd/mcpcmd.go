// Package mcpcmd 提供 mcp 命令：以 stdio 运行 mcpx.Server，以及列出登记的工具。
package mcpcmd

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/go-tenon/tenon/command"
	"github.com/go-tenon/tenon/identity"
	"github.com/go-tenon/tenon/mcpx"
)

// New 返回 mcp 命令：
//
//	mcp serve  以 stdio 运行 srv：本机的 AI 客户端把这个命令作为子进程启动，经标准输入输出通信
//	mcp tools  列出登记的工具、需要的权限和注解，用于检查暴露了什么
//
// sub 是 stdio 模式下调用工具的主体，通常是一个运维账号；它的 Tenant 也是请求所在的租户。
// 命令行没有 token，能运行这个命令的人就以 sub 的身份调用工具，权限仍按每个工具的 Permissions 检查。
//
// 和其他需要数据库的命令一样，在 command.Bind 中声明：New 只保存参数，命令执行时才使用它们。
func New(srv *mcpx.Server, sub identity.Subject) *cobra.Command {
	serve := &cobra.Command{
		Use:   "serve",
		Short: "以 stdio 运行 MCP 服务，供本机的 AI 客户端使用",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return Serve(cmd.Context(), srv, sub, &mcp.StdioTransport{})
		},
	}
	tools := &cobra.Command{
		Use:   "tools",
		Short: "列出 MCP 工具、需要的权限和注解",
		Args:  command.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tPERMISSIONS\tHINTS\tTITLE")
			for _, t := range srv.Tools() {
				perms := strings.Join(t.Permissions, ",")
				if perms == "" {
					perms = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", t.Name, perms, hints(t), t.Title)
			}
			return w.Flush()
		},
	}
	root := &cobra.Command{Use: "mcp", Short: "MCP 服务"}
	root.AddCommand(serve, tools)
	return root
}

// Serve 以 sub 的身份在 transport 上运行 srv，直到客户端断开或 ctx 被取消。mcp serve 用它运行 stdio，
// 测试时传入 mcp.NewInMemoryTransports 的一端。
func Serve(ctx context.Context, srv *mcpx.Server, sub identity.Subject, transport mcp.Transport) error {
	ctx = identity.WithTenant(identity.WithSubject(ctx, sub), sub.Tenant)
	return srv.SDK().Run(ctx, transport)
}

func hints(t mcpx.Tool) string {
	switch {
	case t.ReadOnly:
		return "read-only"
	case t.Destructive && t.Idempotent:
		return "destructive,idempotent"
	case t.Destructive:
		return "destructive"
	case t.Idempotent:
		return "idempotent"
	}
	return "-"
}
