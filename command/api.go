package command

import (
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ryan053857/mygoravel/httpx/rest"
)

// OpenAPI 构造 openapi 命令：以文档模式创建 API、调用 register 注册路由，
// 把 OpenAPI 文档输出到标准输出。不读取配置，不调用处理器工厂。
func OpenAPI(opts rest.Options, register func(api *rest.API)) *cobra.Command {
	return leaf("openapi", "输出 OpenAPI 文档", func(cmd *cobra.Command) error {
		data, err := docAPI(opts, register).OpenAPIJSON()
		if err != nil {
			return err
		}
		_, err = cmd.OutOrStdout().Write(append(data, '\n'))
		return err
	})
}

// Routes 构造 routes 命令：以文档模式创建 API、调用 register 注册路由，输出路由表。
// 支持 --method、--path 过滤；Huma 的文档端点默认隐藏，--all 时以 docs 显示。
func Routes(opts rest.Options, register func(api *rest.API)) *cobra.Command {
	var method, path string
	var all bool
	cmd := leaf("routes", "查看应用的全部路由", func(cmd *cobra.Command) error {
		out := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(out, "METHOD\tPATH\tOPERATION ID\tACCESS\tSUMMARY")
		for _, route := range docAPI(opts, register).Routes() {
			if method != "" && !strings.EqualFold(route.Method, method) ||
				!strings.Contains(route.Path, path) ||
				route.Access == rest.AccessDocs && !all {
				continue
			}
			fmt.Fprintf(out, "%s\t%s\t%s\t%s\t%s\n", route.Method, cell(route.Path),
				orDash(cell(route.OperationID)), cell(access(route)), orDash(cell(route.Summary)))
		}
		return out.Flush()
	})
	cmd.Flags().StringVar(&method, "method", "", "按 HTTP 方法过滤（不区分大小写）")
	cmd.Flags().StringVar(&path, "path", "", "按路径包含的文本过滤")
	cmd.Flags().BoolVar(&all, "all", false, "同时列出 /docs、/openapi.json 等文档端点")
	return cmd
}

// docAPI 以文档模式创建 API 并注册路由。
func docAPI(opts rest.Options, register func(api *rest.API)) *rest.API {
	opts.Mode = rest.ModeOpenAPI
	api := rest.New(opts)
	register(api)
	return api
}

func access(route rest.Route) string {
	if len(route.Scopes) == 0 {
		return route.Access
	}
	return route.Access + ": " + strings.Join(route.Scopes, ", ")
}

// cell 把换行、制表符等空白合并为单个空格，保持每行一条记录。
func cell(value string) string { return strings.Join(strings.Fields(value), " ") }

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
