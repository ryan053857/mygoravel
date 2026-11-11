package aitest

import (
	"context"
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/ryan053857/mygoravel/ai"
)

// PNG 是一张 1x1 的透明 PNG，Imager 默认返回它。
var PNG = ai.Media{Data: mustBase64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="), MIME: "image/png"}

func mustBase64(s string) []byte {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return b
}

// script 记录请求并按顺序给出结果；没有预设结果时用 fallback 生成。
type script[Req, Res any] struct {
	name     string
	mu       sync.Mutex
	results  []Res
	fallback func(Req) Res
	err      error
	requests []Req
}

func (s *script[Req, Res]) call(req Req) (Res, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	var zero Res
	if s.err != nil {
		return zero, s.err
	}
	if s.results == nil {
		return s.fallback(req), nil
	}
	n := len(s.requests)
	if n > len(s.results) {
		return zero, fmt.Errorf("aitest: unexpected %s request %d, script has %d results", s.name, n, len(s.results))
	}
	return s.results[n-1], nil
}

func (s *script[Req, Res]) failWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

func (s *script[Req, Res]) recorded() []Req {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Req(nil), s.requests...)
}

// Imager 实现 ai.Imager，按顺序返回预设的图片，并记录收到的请求。
// 没有预设图片时每次返回 PNG；有预设时用完后返回错误。
type Imager struct {
	s script[ai.ImageRequest, ai.Media]
}

// NewImager 创建依次返回 images 的 Imager。
func NewImager(images ...ai.Media) *Imager {
	return &Imager{script[ai.ImageRequest, ai.Media]{name: "image", results: images, fallback: func(ai.ImageRequest) ai.Media { return PNG }}}
}

// Image 实现 ai.Imager。
func (f *Imager) Image(_ context.Context, req ai.ImageRequest) (ai.Media, error) {
	return f.s.call(req)
}

// FailWith 让之后的请求都返回 err，传 nil 恢复。
func (f *Imager) FailWith(err error) { f.s.failWith(err) }

// Requests 返回目前收到的请求。
func (f *Imager) Requests() []ai.ImageRequest { return f.s.recorded() }

// Speaker 实现 ai.Speaker，按顺序返回预设的音频，并记录收到的请求。
// 没有预设音频时，每次返回以朗读文字为内容、类型为 audio/mpeg 的音频；有预设时用完后返回错误。
type Speaker struct {
	s script[ai.SpeechRequest, ai.Media]
}

// NewSpeaker 创建依次返回 audios 的 Speaker。
func NewSpeaker(audios ...ai.Media) *Speaker {
	return &Speaker{script[ai.SpeechRequest, ai.Media]{name: "speech", results: audios, fallback: func(req ai.SpeechRequest) ai.Media {
		return ai.Media{Data: []byte(req.Text), MIME: "audio/mpeg"}
	}}}
}

// Speak 实现 ai.Speaker。
func (f *Speaker) Speak(_ context.Context, req ai.SpeechRequest) (ai.Media, error) {
	return f.s.call(req)
}

// FailWith 让之后的请求都返回 err，传 nil 恢复。
func (f *Speaker) FailWith(err error) { f.s.failWith(err) }

// Requests 返回目前收到的请求。
func (f *Speaker) Requests() []ai.SpeechRequest { return f.s.recorded() }

// Transcriber 实现 ai.Transcriber，按顺序返回预设的文字，并记录收到的请求。
// 没有预设文字时，每次把音频内容当作文字返回；有预设时用完后返回错误。
type Transcriber struct {
	s script[ai.TranscriptionRequest, string]
}

// NewTranscriber 创建依次返回 texts 的 Transcriber。
func NewTranscriber(texts ...string) *Transcriber {
	return &Transcriber{script[ai.TranscriptionRequest, string]{name: "transcription", results: texts, fallback: func(req ai.TranscriptionRequest) string {
		return string(req.Audio.Data)
	}}}
}

// Transcribe 实现 ai.Transcriber。
func (f *Transcriber) Transcribe(_ context.Context, req ai.TranscriptionRequest) (string, error) {
	return f.s.call(req)
}

// FailWith 让之后的请求都返回 err，传 nil 恢复。
func (f *Transcriber) FailWith(err error) { f.s.failWith(err) }

// Requests 返回目前收到的请求。
func (f *Transcriber) Requests() []ai.TranscriptionRequest { return f.s.recorded() }

// Embedder 实现 ai.Embedder，由文本的哈希生成单位长度的向量：相同的文本总是得到相同的向量，不同的文本几乎正交。
type Embedder struct {
	dims int
	s    script[[]string, [][]float32]
}

// NewEmbedder 创建生成 dims 维向量的 Embedder。
func NewEmbedder(dims int) *Embedder {
	e := &Embedder{dims: dims}
	e.s = script[[]string, [][]float32]{name: "embedding", fallback: func(texts []string) [][]float32 {
		out := make([][]float32, len(texts))
		for i, t := range texts {
			out[i] = Vector(t, dims)
		}
		return out
	}}
	return e
}

// Embed 实现 ai.Embedder。
func (f *Embedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	return f.s.call(append([]string(nil), texts...))
}

// FailWith 让之后的请求都返回 err，传 nil 恢复。
func (f *Embedder) FailWith(err error) { f.s.failWith(err) }

// Requests 返回每次请求的文本。
func (f *Embedder) Requests() [][]string { return f.s.recorded() }

// Vector 返回 Embedder 为 text 生成的 dims 维向量，用于在测试中构造期望值。
func Vector(text string, dims int) []float32 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	seed := h.Sum64()
	r := rand.New(rand.NewPCG(seed, seed>>1|1))
	v := make([]float32, dims)
	var norm float64
	for i := range v {
		f := r.NormFloat64()
		v[i] = float32(f)
		norm += f * f
	}
	norm = math.Sqrt(norm)
	for i := range v {
		v[i] = float32(float64(v[i]) / norm)
	}
	return v
}

// RerankRequest 是 Reranker 收到的一次请求。
type RerankRequest struct {
	Query string
	Docs  []string
	Limit int
}

// Reranker 实现 ai.Reranker，按顺序返回预设的结果，并记录收到的请求。
// 没有预设结果时，按查询中的字符在文档中出现的比例打分，分数相同的保持原来的顺序；有预设时用完后返回错误。
type Reranker struct {
	s script[RerankRequest, []ai.Ranked]
}

// NewReranker 创建依次返回 results 的 Reranker。
func NewReranker(results ...[]ai.Ranked) *Reranker {
	return &Reranker{script[RerankRequest, []ai.Ranked]{name: "rerank", results: results, fallback: overlap}}
}

func overlap(req RerankRequest) []ai.Ranked {
	chars := map[rune]bool{}
	for _, r := range req.Query {
		if !unicode.IsSpace(r) {
			chars[unicode.ToLower(r)] = true
		}
	}
	out := make([]ai.Ranked, len(req.Docs))
	for i, d := range req.Docs {
		hits := 0
		lower := strings.ToLower(d)
		for r := range chars {
			if strings.ContainsRune(lower, r) {
				hits++
			}
		}
		out[i] = ai.Ranked{Index: i}
		if len(chars) > 0 {
			out[i].Score = float64(hits) / float64(len(chars))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if req.Limit > 0 && req.Limit < len(out) {
		out = out[:req.Limit]
	}
	return out
}

// Rerank 实现 ai.Reranker。
func (f *Reranker) Rerank(_ context.Context, query string, docs []string, limit int) ([]ai.Ranked, error) {
	return f.s.call(RerankRequest{Query: query, Docs: append([]string(nil), docs...), Limit: limit})
}

// FailWith 让之后的请求都返回 err，传 nil 恢复。
func (f *Reranker) FailWith(err error) { f.s.failWith(err) }

// Requests 返回目前收到的请求。
func (f *Reranker) Requests() []RerankRequest { return f.s.recorded() }

// Decider 实现 ai.Decider，按顺序使用预设的回答，并记录收到的请求。每份预设按问题名称给出回答，
// 预设中没有的问题、或没有预设时的所有问题按默认值回答：是非题概率 0.5，选择题选第一个选项，打分题为最低一级。
// 有预设时用完后返回错误。
type Decider struct {
	s script[ai.DecisionRequest, map[string]ai.Answer]
}

// NewDecider 创建依次使用 results 回答的 Decider。
func NewDecider(results ...map[string]ai.Answer) *Decider {
	return &Decider{script[ai.DecisionRequest, map[string]ai.Answer]{name: "decision", results: results, fallback: func(ai.DecisionRequest) map[string]ai.Answer { return nil }}}
}

// Decide 实现 ai.Decider。
func (f *Decider) Decide(_ context.Context, req ai.DecisionRequest) (ai.Decision, error) {
	if err := req.Validate(); err != nil {
		return ai.Decision{}, err
	}
	preset, err := f.s.call(req)
	if err != nil {
		return ai.Decision{}, err
	}
	out := ai.Decision{Answers: map[string]ai.Answer{}, Model: "aitest"}
	for _, q := range req.Questions {
		a, ok := preset[q.Name]
		if !ok {
			a = defaultAnswer(q)
		}
		out.Answers[q.Name] = ai.NewAnswer(q, a)
	}
	return out, nil
}

func defaultAnswer(q ai.Question) ai.Answer {
	switch q.Kind {
	case ai.ChoiceQuestion:
		p := make([]float64, len(q.Options))
		p[0] = 1
		return ai.Answer{Choice: q.Options[0].Value, Probabilities: p, Confidence: 1}
	case ai.ScoreQuestion:
		p := make([]float64, len(q.Levels))
		p[0] = 1
		return ai.Answer{Probabilities: p, Confidence: 1}
	}
	return ai.Answer{Probability: 0.5}
}

// FailWith 让之后的请求都返回 err，传 nil 恢复。
func (f *Decider) FailWith(err error) { f.s.failWith(err) }

// Requests 返回目前收到的请求。
func (f *Decider) Requests() []ai.DecisionRequest { return f.s.recorded() }
