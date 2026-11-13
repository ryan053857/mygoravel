package chat

import (
	"github.com/openai/openai-go/option"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"

	"github.com/go-tenon/tenon/ai"
)

// NewModel 按配置创建模型，opts 原样传给 openai.New。其他接口的模型直接使用 trpc-agent-go 的对应包创建。
func NewModel(cfg ai.ModelConfig, opts ...openai.Option) model.Model {
	base := []openai.Option{
		openai.WithAPIKey(cfg.APIKey),
		openai.WithOpenAIOptions(option.WithMaxRetries(cfg.Retries())),
	}
	if cfg.BaseURL != "" {
		base = append(base, openai.WithBaseURL(cfg.BaseURL))
	}
	return openai.New(cfg.Model, append(base, opts...)...)
}
