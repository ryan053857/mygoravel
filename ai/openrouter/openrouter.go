// Package openrouter 通过 OpenRouter 生成图片、合成语音、识别语音和计算向量。
//
// 语音和向量使用 OpenAI 兼容接口，与 ai/openai 相同；图片使用 OpenRouter 自己的 /images 接口，
// 按宽高比例而不是像素尺寸指定大小。
package openrouter

import (
	"context"
	"encoding/base64"
	"net/http"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/openai"
)

const defaultBaseURL = "https://openrouter.ai/api/v1"

// Client 是 OpenRouter 上的一个模型，实现 ai.Imager、ai.Speaker、ai.Transcriber 和 ai.Embedder。
// 创建 Client 不访问网络，可以每次请求时按当时的配置创建。
type Client struct {
	*openai.Client
}

var (
	_ ai.Imager      = (*Client)(nil)
	_ ai.Speaker     = (*Client)(nil)
	_ ai.Transcriber = (*Client)(nil)
	_ ai.Embedder    = (*Client)(nil)
)

// New 按 cfg 创建 Client，BaseURL 为空时使用 OpenRouter 的地址。opts 与 ai/openai 相同，
// 其中 WithImageSizes 对图片不起作用。
func New(cfg ai.ModelConfig, opts ...openai.Option) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	return &Client{openai.New(cfg, opts...)}
}

var aspectRatios = map[ai.Aspect]string{ai.Square: "1:1", ai.Portrait: "2:3", ai.Landscape: "3:2"}

type imageRef struct {
	Type     string `json:"type"`
	ImageURL struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

type imageParams struct {
	Model           string     `json:"model"`
	Prompt          string     `json:"prompt"`
	AspectRatio     string     `json:"aspect_ratio,omitempty"`
	Quality         string     `json:"quality,omitempty"`
	InputReferences []imageRef `json:"input_references,omitempty"`
}

type imageResponse struct {
	Data []struct {
		B64JSON   string `json:"b64_json"`
		MediaType string `json:"media_type"`
	} `json:"data"`
}

// Image 实现 ai.Imager。有参考图时按参考图编辑或改绘。
func (c *Client) Image(ctx context.Context, req ai.ImageRequest) (ai.Media, error) {
	if req.Prompt == "" {
		return ai.Media{}, errors.New("ai/openrouter: image prompt is required")
	}
	p := imageParams{Model: c.Model(), Prompt: req.Prompt, Quality: req.Quality}
	if req.Aspect != "" {
		var ok bool
		if p.AspectRatio, ok = aspectRatios[req.Aspect]; !ok {
			return ai.Media{}, errors.Newf("ai/openrouter: unknown aspect %q", req.Aspect)
		}
	}
	for _, ref := range req.References {
		var r imageRef
		r.Type = "image_url"
		r.ImageURL.URL = "data:" + ref.MIME + ";base64," + base64.StdEncoding.EncodeToString(ref.Data)
		p.InputReferences = append(p.InputReferences, r)
	}
	var resp imageResponse
	if err := c.OpenAI().Post(ctx, "images", p, &resp); err != nil {
		return ai.Media{}, errors.Wrap(err, "ai/openrouter: generate image")
	}
	if len(resp.Data) == 0 || resp.Data[0].B64JSON == "" {
		return ai.Media{}, errors.New("ai/openrouter: no image returned")
	}
	data, err := base64.StdEncoding.DecodeString(resp.Data[0].B64JSON)
	if err != nil {
		return ai.Media{}, errors.Wrap(err, "ai/openrouter: decode image")
	}
	mime := resp.Data[0].MediaType
	if mime == "" {
		mime = http.DetectContentType(data)
	}
	return ai.Media{Data: data, MIME: mime}, nil
}
