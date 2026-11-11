// Package systemone 用 System One 格式调用决策模型，接口是 POST {BaseURL}/systemone。
//
// 这种格式由 TypeSafe 提出，阿里云百炼（BaseURL 形如 https://<工作空间>.cn-beijing.maas.aliyuncs.com/compatible-mode/v1）、
// TypeSafe（https://api.typesafe.ai/v1）和 OpenRouter（ai/openrouter 已经使用它）都提供。
// OpenAI 自己的决策接口格式不同，由 ai/openai 实现。
package systemone

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/openai"
)

// Client 是提供 System One 接口的一个决策模型。它嵌入 openai.Client，同一个地址上的其他 OpenAI 兼容接口照常可用，
// 只有 Decide 改用 System One 格式。
type Client struct {
	*openai.Client
}

var _ ai.Decider = (*Client)(nil)

// New 按 cfg 创建 Client，opts 与 ai/openai 相同。
func New(cfg ai.ModelConfig, opts ...openai.Option) *Client { return &Client{openai.New(cfg, opts...)} }

// Wrap 让 c 的 Decide 改用 System One 格式。
func Wrap(c *openai.Client) *Client { return &Client{c} }

type answer struct {
	Type          string             `json:"type"`
	Noul          float64            `json:"noul"`
	Choice        string             `json:"choice"`
	Score         float64            `json:"score"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

type response struct {
	Model   string            `json:"model"`
	Answers map[string]answer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Decide 实现 ai.Decider。
func (c *Client) Decide(ctx context.Context, req ai.DecisionRequest) (ai.Decision, error) {
	if err := req.Validate(); err != nil {
		return ai.Decision{}, err
	}
	body := object{{"model", c.Model()}, {"state", state(req)}, {"questions", questions(req.Questions)}}
	var resp response
	if err := c.OpenAI().Post(ctx, "systemone", body, &resp); err != nil {
		return ai.Decision{}, errors.Wrap(err, "ai/systemone: decide")
	}
	out := ai.Decision{Answers: map[string]ai.Answer{}, Model: resp.Model, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}
	for _, q := range req.Questions {
		a, ok := resp.Answers[q.Name]
		if !ok {
			return ai.Decision{}, errors.Newf("ai/systemone: no answer for question %q", q.Name)
		}
		got, err := answerOf(q, a)
		if err != nil {
			return ai.Decision{}, err
		}
		out.Answers[q.Name] = got
	}
	return out, nil
}

// state 在没有图片时原样发送材料，否则发送数组：材料，再依次是图片，命名的图片前面加上名称。
func state(req ai.DecisionRequest) any {
	if len(req.Images) == 0 {
		return req.Input
	}
	parts := []any{req.Input}
	for _, img := range req.Images {
		if img.Name != "" {
			parts = append(parts, img.Name+":")
		}
		parts = append(parts, object{{"type", "image_url"}, {"image_url", object{{"url", openai.DataURL(img)}}}})
	}
	return parts
}

func questions(qs []ai.Question) object {
	out := make(object, len(qs))
	for i, q := range qs {
		m := object{{"type", ""}, {"instructions", q.Instructions}}
		switch q.Kind {
		case ai.YesNoQuestion:
			m[0].v = "noul"
			if q.IfTrue != "" || q.IfFalse != "" {
				m = append(m, field{"criteria", object{{"true", q.IfTrue}, {"false", q.IfFalse}}})
			}
		case ai.ChoiceQuestion:
			m[0].v = "choice"
			criteria := make(object, len(q.Options))
			for j, o := range q.Options {
				desc := o.Description
				if desc == "" {
					desc = o.Value
				}
				criteria[j] = field{o.Value, desc}
			}
			m = append(m, field{"criteria", criteria})
		case ai.ScoreQuestion:
			m[0].v = "score"
			criteria := make([]string, len(q.Levels))
			for j, l := range q.Levels {
				criteria[j] = l.Label
				if l.Description != "" {
					criteria[j] += "：" + l.Description
				}
			}
			m = append(m, field{"criteria", criteria})
		}
		out[i] = field{q.Name, m}
	}
	return out
}

func answerOf(q ai.Question, a answer) (ai.Answer, error) {
	if a.Type == "refusal" {
		return ai.NewAnswer(q, ai.Answer{Refused: true}), nil
	}
	want := map[ai.QuestionKind]string{ai.YesNoQuestion: "noul", ai.ChoiceQuestion: "choice", ai.ScoreQuestion: "score"}[q.Kind]
	if a.Type != want {
		return ai.Answer{}, errors.Newf("ai/systemone: question %q answered as %q, want %q", q.Name, a.Type, want)
	}
	out := ai.Answer{Probability: a.Noul, Choice: a.Choice, Score: a.Score, Confidence: a.Confidence}
	switch q.Kind {
	case ai.ChoiceQuestion:
		out.Probabilities = make([]float64, len(q.Options))
		for i, o := range q.Options {
			out.Probabilities[i] = a.Probabilities[o.Value]
		}
	case ai.ScoreQuestion:
		out.Probabilities = make([]float64, len(q.Levels))
		for k, p := range a.Probabilities {
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(q.Levels) {
				return ai.Answer{}, errors.Newf("ai/systemone: question %q has unknown level %q", q.Name, k)
			}
			out.Probabilities[i] = p
		}
	}
	return ai.NewAnswer(q, out), nil
}

// object 是按顺序编码键的 JSON 对象：选择题的选项按传入的顺序发给模型。
type object []field

type field struct {
	k string
	v any
}

func (o object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, f := range o {
		if i > 0 {
			b.WriteByte(',')
		}
		k, err := json.Marshal(f.k)
		if err != nil {
			return nil, err
		}
		v, err := json.Marshal(f.v)
		if err != nil {
			return nil, err
		}
		b.Write(k)
		b.WriteByte(':')
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}
