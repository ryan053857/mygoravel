// Package openai 通过 OpenAI 兼容接口生成图片、合成语音、识别语音和计算向量。
//
// OpenAI、火山方舟、硅基流动、通义千问的兼容模式、Ollama 等都提供这类接口，但每家支持的能力不同，以厂商文档为准。
package openai

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/cockroachdb/errors"
	oa "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ryan053857/mygoravel/ai"
)

const defaultBaseURL = "https://api.openai.com/v1/"

// Client 是 OpenAI 兼容接口上的一个模型。它实现 ai.Imager、ai.Speaker、ai.Transcriber 和 ai.Embedder，
// 能用哪一种取决于模型本身。创建 Client 不访问网络，可以每次请求时按当时的配置创建。
type Client struct {
	model     string
	client    oa.Client
	http      *http.Client
	extra     []option.RequestOption
	sizes     map[ai.Aspect]string
	dims      int
	batchSize int
}

var (
	_ ai.Imager      = (*Client)(nil)
	_ ai.Speaker     = (*Client)(nil)
	_ ai.Transcriber = (*Client)(nil)
	_ ai.Embedder    = (*Client)(nil)
)

// Option 调整 Client。
type Option func(*Client)

// WithDimensions 设置向量的维度，模型支持缩减维度时有效，例如 text-embedding-3 系列。
func WithDimensions(n int) Option { return func(c *Client) { c.dims = n } }

// WithBatchSize 设置一次请求最多计算多少段文本的向量，超过时分批请求。默认一次全部发送；
// 有的厂商限制每批的数量，例如通义千问的兼容模式每批最多 10 段。
func WithBatchSize(n int) Option { return func(c *Client) { c.batchSize = n } }

// WithImageSizes 设置各宽高比例对应的尺寸，例如 {ai.Square: "2048x2048"}，用于尺寸规格与 OpenAI 不同的模型。
// 没有列出的比例使用默认值。
func WithImageSizes(sizes map[ai.Aspect]string) Option {
	return func(c *Client) {
		for k, v := range sizes {
			c.sizes[k] = v
		}
	}
}

// WithHTTPClient 设置发送请求和下载图片使用的 HTTP 客户端。
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.http = hc
		c.extra = append(c.extra, option.WithHTTPClient(hc))
	}
}

// WithRequestOptions 追加 openai-go 的请求选项，例如用 option.WithJSONSet 传厂商自定义的参数。
func WithRequestOptions(opts ...option.RequestOption) Option {
	return func(c *Client) { c.extra = append(c.extra, opts...) }
}

// New 按 cfg 创建 Client。BaseURL 为空时使用 OpenAI。
func New(cfg ai.ModelConfig, opts ...Option) *Client {
	c := &Client{model: cfg.Model, http: http.DefaultClient, sizes: imageSizes(cfg.Model)}
	for _, opt := range opts {
		opt(c)
	}
	base := cfg.BaseURL
	if base == "" {
		base = defaultBaseURL
	}
	ro := append([]option.RequestOption{option.WithBaseURL(base), option.WithAPIKey(cfg.APIKey)}, c.extra...)
	c.client = oa.NewClient(ro...)
	return c
}

// Model 返回模型名。
func (c *Client) Model() string { return c.model }

// OpenAI 返回底层的 openai-go 客户端，已经设置好接口地址和密钥，用于调用厂商自己的接口。
func (c *Client) OpenAI() *oa.Client { return &c.client }

func isDallE(model string) bool { return strings.HasPrefix(model, "dall-e") }

func imageSizes(model string) map[ai.Aspect]string {
	if model == "dall-e-3" {
		return map[ai.Aspect]string{ai.Square: "1024x1024", ai.Portrait: "1024x1792", ai.Landscape: "1792x1024"}
	}
	return map[ai.Aspect]string{ai.Square: "1024x1024", ai.Portrait: "1024x1536", ai.Landscape: "1536x1024"}
}

// Image 实现 ai.Imager。有参考图时调用图片编辑接口，否则调用图片生成接口。
func (c *Client) Image(ctx context.Context, req ai.ImageRequest) (ai.Media, error) {
	if req.Prompt == "" {
		return ai.Media{}, errors.New("ai/openai: image prompt is required")
	}
	size := ""
	if req.Aspect != "" {
		var ok bool
		if size, ok = c.sizes[req.Aspect]; !ok {
			return ai.Media{}, errors.Newf("ai/openai: unknown aspect %q", req.Aspect)
		}
	}
	quality := req.Quality
	switch {
	case c.model == "dall-e-2":
		quality = ""
	case c.model == "dall-e-3" && quality == "high":
		quality = "hd"
	case c.model == "dall-e-3" && quality != "":
		quality = "standard"
	}

	var (
		resp *oa.ImagesResponse
		err  error
	)
	if len(req.References) == 0 {
		p := oa.ImageGenerateParams{
			Prompt:  req.Prompt,
			Model:   oa.ImageModel(c.model),
			Size:    oa.ImageGenerateParamsSize(size),
			Quality: oa.ImageGenerateParamsQuality(quality),
		}
		if isDallE(c.model) {
			p.ResponseFormat = oa.ImageGenerateParamsResponseFormatB64JSON
		}
		resp, err = c.client.Images.Generate(ctx, p)
	} else {
		files := make([]io.Reader, len(req.References))
		for i, ref := range req.References {
			files[i] = oa.File(bytes.NewReader(ref.Data), fmt.Sprintf("image-%d.%s", i+1, extension(ref.MIME, "png")), ref.MIME)
		}
		image := oa.ImageEditParamsImageUnion{OfFileArray: files}
		if len(files) == 1 {
			image = oa.ImageEditParamsImageUnion{OfFile: files[0]}
		}
		p := oa.ImageEditParams{
			Image:   image,
			Prompt:  req.Prompt,
			Model:   oa.ImageModel(c.model),
			Size:    oa.ImageEditParamsSize(size),
			Quality: oa.ImageEditParamsQuality(quality),
		}
		if isDallE(c.model) {
			p.ResponseFormat = oa.ImageEditParamsResponseFormatB64JSON
		}
		resp, err = c.client.Images.Edit(ctx, p)
	}
	if err != nil {
		return ai.Media{}, errors.Wrap(err, "ai/openai: generate image")
	}
	if len(resp.Data) == 0 {
		return ai.Media{}, errors.New("ai/openai: no image returned")
	}
	data, err := c.imageData(ctx, resp.Data[0])
	if err != nil {
		return ai.Media{}, err
	}
	return ai.Media{Data: data, MIME: http.DetectContentType(data)}, nil
}

// imageData 取出图片内容：OpenAI 返回 base64，有的厂商只返回一个临时链接。
func (c *Client) imageData(ctx context.Context, img oa.Image) ([]byte, error) {
	if img.B64JSON != "" {
		data, err := base64.StdEncoding.DecodeString(img.B64JSON)
		return data, errors.Wrap(err, "ai/openai: decode image")
	}
	if img.URL == "" {
		return nil, errors.New("ai/openai: image has neither data nor url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, img.URL, nil)
	if err != nil {
		return nil, errors.Wrap(err, "ai/openai: download image")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "ai/openai: download image")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Newf("ai/openai: download image: %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	return data, errors.Wrap(err, "ai/openai: download image")
}

// Speak 实现 ai.Speaker。
func (c *Client) Speak(ctx context.Context, req ai.SpeechRequest) (ai.Media, error) {
	if req.Text == "" {
		return ai.Media{}, errors.New("ai/openai: speech text is required")
	}
	if req.Voice == "" {
		return ai.Media{}, errors.New("ai/openai: speech voice is required")
	}
	p := oa.AudioSpeechNewParams{
		Input:          req.Text,
		Model:          oa.SpeechModel(c.model),
		Voice:          oa.AudioSpeechNewParamsVoice(req.Voice),
		ResponseFormat: oa.AudioSpeechNewParamsResponseFormat(req.Format),
	}
	if req.Instructions != "" {
		p.Instructions = oa.String(req.Instructions)
	}
	resp, err := c.client.Audio.Speech.New(ctx, p)
	if err != nil {
		return ai.Media{}, errors.Wrap(err, "ai/openai: synthesize speech")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return ai.Media{}, errors.Wrap(err, "ai/openai: synthesize speech")
	}
	mime := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(mime, "audio/") { // 有的厂商返回 application/octet-stream
		mime = audioMIME(req.Format)
	}
	return ai.Media{Data: data, MIME: mime}, nil
}

// Transcribe 实现 ai.Transcriber。
func (c *Client) Transcribe(ctx context.Context, req ai.TranscriptionRequest) (string, error) {
	if len(req.Audio.Data) == 0 {
		return "", errors.New("ai/openai: audio is required")
	}
	p := oa.AudioTranscriptionNewParams{
		File:  oa.File(bytes.NewReader(req.Audio.Data), "audio."+extension(req.Audio.MIME, "mp3"), req.Audio.MIME),
		Model: oa.AudioModel(c.model),
	}
	if req.Language != "" {
		p.Language = oa.String(req.Language)
	}
	if req.Prompt != "" {
		p.Prompt = oa.String(req.Prompt)
	}
	t, err := c.client.Audio.Transcriptions.New(ctx, p)
	if err != nil {
		return "", errors.Wrap(err, "ai/openai: transcribe audio")
	}
	return t.Text, nil
}

// Embed 实现 ai.Embedder。
func (c *Client) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	size := c.batchSize
	if size <= 0 {
		size = len(texts)
	}
	for start := 0; start < len(texts); start += size {
		part := texts[start:min(start+size, len(texts))]
		p := oa.EmbeddingNewParams{
			Input:          oa.EmbeddingNewParamsInputUnion{OfArrayOfStrings: part},
			Model:          oa.EmbeddingModel(c.model),
			EncodingFormat: oa.EmbeddingNewParamsEncodingFormatFloat,
		}
		if c.dims > 0 {
			p.Dimensions = oa.Int(int64(c.dims))
		}
		resp, err := c.client.Embeddings.New(ctx, p)
		if err != nil {
			return nil, errors.Wrap(err, "ai/openai: embed")
		}
		for _, d := range resp.Data {
			i := int(d.Index)
			if i < 0 || i >= len(part) {
				return nil, errors.Newf("ai/openai: embedding index %d out of range", i)
			}
			v := make([]float32, len(d.Embedding))
			for j, f := range d.Embedding {
				v[j] = float32(f)
			}
			out[start+i] = v
		}
	}
	for i, v := range out {
		if v == nil {
			return nil, errors.Newf("ai/openai: no embedding returned for text %d", i)
		}
	}
	return out, nil
}

var extensions = map[string]string{
	"image/png":    "png",
	"image/jpeg":   "jpg",
	"image/webp":   "webp",
	"image/gif":    "gif",
	"audio/mpeg":   "mp3",
	"audio/mp3":    "mp3",
	"audio/wav":    "wav",
	"audio/x-wav":  "wav",
	"audio/wave":   "wav",
	"audio/webm":   "webm",
	"audio/ogg":    "ogg",
	"audio/opus":   "opus",
	"audio/flac":   "flac",
	"audio/x-flac": "flac",
	"audio/mp4":    "m4a",
	"audio/x-m4a":  "m4a",
	"audio/aac":    "aac",
	"video/mp4":    "mp4",
	"video/webm":   "webm",
}

// extension 返回 MIME 对应的扩展名。模型服务按文件名判断格式，所以上传时要带上正确的扩展名。
func extension(mime, fallback string) string {
	mime, _, _ = strings.Cut(mime, ";")
	if ext, ok := extensions[strings.TrimSpace(strings.ToLower(mime))]; ok {
		return ext
	}
	return fallback
}

func audioMIME(format string) string {
	switch format {
	case "", "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "opus":
		return "audio/opus"
	case "aac":
		return "audio/aac"
	case "flac":
		return "audio/flac"
	case "pcm":
		return "audio/pcm"
	}
	return "application/octet-stream"
}
