package ai

import (
	"context"

	"github.com/cockroachdb/errors"
)

// Media 是一段图片或音频。
type Media struct {
	// Data 是文件内容。
	Data []byte
	// MIME 是内容类型，例如 image/png、audio/mpeg。
	MIME string
}

// Aspect 是图片的宽高比例。
type Aspect string

// 图片的宽高比例。为空时使用模型的默认尺寸。
const (
	Square    Aspect = "square"
	Portrait  Aspect = "portrait"
	Landscape Aspect = "landscape"
)

// ImageRequest 描述要生成的图片。
type ImageRequest struct {
	// Prompt 描述图片内容。
	Prompt string
	// Aspect 是宽高比例，为空时使用模型的默认尺寸。
	Aspect Aspect
	// Quality 是 high、medium 或 low，为空时使用模型的默认质量。
	Quality string
	// References 是参考图。不为空时按这些图片编辑或改绘。
	References []Media
}

// Imager 根据描述生成图片。
type Imager interface {
	Image(ctx context.Context, req ImageRequest) (Media, error)
}

// SpeechRequest 描述要合成的语音。
type SpeechRequest struct {
	// Text 是要朗读的文字。
	Text string
	// Voice 是音色，取值由模型决定，例如 OpenAI 的 alloy。
	Voice string
	// Instructions 描述语气和风格，例如“像在讲睡前故事”，只有部分模型支持。
	Instructions string
	// Format 是音频格式，例如 mp3、wav、opus，为空时使用模型的默认格式。
	Format string
}

// Speaker 把文字合成为语音。
type Speaker interface {
	Speak(ctx context.Context, req SpeechRequest) (Media, error)
}

// TranscriptionRequest 描述要识别的音频。
type TranscriptionRequest struct {
	// Audio 是音频，MIME 用于告诉模型服务音频格式。
	Audio Media
	// Language 是音频的语言，例如 zh、en，为空时自动识别。
	Language string
	// Prompt 是提示词，例如音频中会出现的专有名词。
	Prompt string
}

// Transcriber 把语音识别为文字。
type Transcriber interface {
	Transcribe(ctx context.Context, req TranscriptionRequest) (string, error)
}

// Embedder 把文本转换为向量，返回的向量与 texts 一一对应。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Ranked 是重排后的一项。
type Ranked struct {
	// Index 是它在传入的文档中的位置。
	Index int
	// Score 是与查询的相关度，越大越相关，取值范围由模型决定。
	Score float64
}

// Reranker 按与 query 的相关度给 docs 排序，返回最相关的 limit 项，按相关度从高到低排列；limit 不大于 0 时返回全部。
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string, limit int) ([]Ranked, error)
}

// RerankBy 用 r 给 items 排序，text 取出每一项用于比较的文本，返回最相关的 limit 项。
func RerankBy[T any](ctx context.Context, r Reranker, query string, items []T, text func(T) string, limit int) ([]T, error) {
	if len(items) == 0 {
		return nil, nil
	}
	docs := make([]string, len(items))
	for i, it := range items {
		docs[i] = text(it)
	}
	ranked, err := r.Rerank(ctx, query, docs, limit)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(ranked))
	for _, rk := range ranked {
		if rk.Index < 0 || rk.Index >= len(items) {
			return nil, errors.Newf("ai: rerank index %d out of range", rk.Index)
		}
		out = append(out, items[rk.Index])
	}
	return out, nil
}
