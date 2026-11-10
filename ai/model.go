package ai

import (
	"github.com/cockroachdb/errors"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// ModelConfig 是一个 OpenAI 兼容接口的模型，DeepSeek、通义千问、Kimi、OpenAI 等都提供这种接口。
type ModelConfig struct {
	// Model 是模型名，例如 deepseek-chat。
	Model string `yaml:"model"`
	// BaseURL 是接口地址，例如 https://api.deepseek.com；为空时使用 OpenAI。
	BaseURL string `yaml:"base_url"`
	// APIKey 是接口密钥。
	APIKey string `yaml:"api_key"`
}

// Validate 检查模型名和密钥已经设置。
func (c ModelConfig) Validate() error {
	var errs []error
	if c.Model == "" {
		errs = append(errs, errors.New("model is required"))
	}
	if c.APIKey == "" {
		errs = append(errs, errors.New("api_key is required"))
	}
	return errors.Join(errs...)
}

// NewModel 按配置创建模型，opts 原样传给 openai.New。其他接口的模型直接使用 trpc-agent-go 的对应包创建。
func NewModel(cfg ModelConfig, opts ...openai.Option) model.Model {
	base := []openai.Option{openai.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		base = append(base, openai.WithBaseURL(cfg.BaseURL))
	}
	return openai.New(cfg.Model, append(base, opts...)...)
}
