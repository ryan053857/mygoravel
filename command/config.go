package command

import (
	"encoding/json"

	"github.com/cockroachdb/errors"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v4"
)

// Config 构造 config check/show 命令，C 是应用的根配置结构体。
// check 只加载并校验配置；show 输出加载后的配置，--format 为 yaml（默认）或 json。
func Config[C any]() *cobra.Command {
	var format string
	show := leaf("show", "输出加载后的配置", func(cmd *cobra.Command) error {
		if format != "yaml" && format != "json" {
			return AsExitError(ExitUsage, errors.Newf("invalid --format %q: must be yaml or json", format))
		}
		cfg, err := LoadConfig[C](cmd)
		if err != nil {
			return err
		}
		if format == "json" {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		}
		enc := yaml.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent(2)
		if err := enc.Encode(cfg); err != nil {
			return err
		}
		return enc.Close()
	})
	show.Flags().StringVar(&format, "format", "yaml", "输出格式：yaml 或 json")
	return group("config", "配置工具",
		leaf("check", "加载并校验配置，不访问网络", func(cmd *cobra.Command) error {
			_, err := LoadConfig[C](cmd)
			return err
		}),
		show,
	)
}
