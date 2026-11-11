package ai

import (
	"github.com/cockroachdb/errors"
	"github.com/openai/openai-go/option"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/model/openai"
)

// ModelConfig 描述 OpenAI 兼容接口上的一个模型，DeepSeek、通义千问、Kimi、OpenAI 等都提供这种接口。
// 它是普通的值：可以放进配置文件，也可以由应用从数据库等处查出后现场构造。
type ModelConfig struct {
	// Model 是模型名，例如 deepseek-chat。
	Model string `yaml:"model"`
	// BaseURL 是接口地址，例如 https://api.deepseek.com；为空时使用 OpenAI。
	BaseURL string `yaml:"base_url"`
	// APIKey 是接口密钥。
	APIKey string `yaml:"api_key"`
	// MaxRetries 是请求失败后最多重试的次数：连接失败、408、409、429 和 5xx 时，按服务端的 Retry-After
	// 或指数退避（0.5 秒起，最长 8 秒）等待后重试，用完后返回错误。0 使用默认的 2 次，-1 表示不重试。
	// 模型已经开始流式返回内容后出错不会重试。
	MaxRetries int `yaml:"max_retries"`
}

// defaultRetries 是 MaxRetries 为 0 时的重试次数，与 openai-go 的默认值相同。
const defaultRetries = 2

// Retries 返回实际的重试次数：MaxRetries 为 0 时是默认的 2 次，为负数时是 0。
func (c ModelConfig) Retries() int {
	switch {
	case c.MaxRetries == 0:
		return defaultRetries
	case c.MaxRetries < 0:
		return 0
	}
	return c.MaxRetries
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
	base := []openai.Option{
		openai.WithAPIKey(cfg.APIKey),
		openai.WithOpenAIOptions(option.WithMaxRetries(cfg.Retries())),
	}
	if cfg.BaseURL != "" {
		base = append(base, openai.WithBaseURL(cfg.BaseURL))
	}
	return openai.New(cfg.Model, append(base, opts...)...)
}
