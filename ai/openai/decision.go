package openai

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/ai"
)

var _ ai.Decider = (*Client)(nil)

// DataURL 把图片编码为 data URL。
func DataURL(m ai.Media) string {
	return "data:" + m.MIME + ";base64," + base64.StdEncoding.EncodeToString(m.Data)
}

type decisionAnswer struct {
	Type          string  `json:"type"`
	Name          string  `json:"name"`
	Probability   float64 `json:"probability"`
	Choice        string  `json:"choice"`
	Score         float64 `json:"score"`
	Confidence    float64 `json:"confidence"`
	Probabilities []struct {
		Value       json.RawMessage `json:"value"`
		Probability float64         `json:"probability"`
	} `json:"probabilities"`
}

type decisionResponse struct {
	Model   string           `json:"model"`
	Answers []decisionAnswer `json:"answers"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

// Decide 实现 ai.Decider，调用 OpenAI 的 POST {BaseURL}/decisions。
func (c *Client) Decide(ctx context.Context, req ai.DecisionRequest) (ai.Decision, error) {
	if err := req.Validate(); err != nil {
		return ai.Decision{}, err
	}
	body := map[string]any{"model": c.model, "input": decisionInput(req), "questions": decisionQuestions(req.Questions)}
	var resp decisionResponse
	if err := c.client.Post(ctx, "decisions", body, &resp); err != nil {
		return ai.Decision{}, errors.Wrap(err, "ai/openai: decide")
	}
	got := map[string]decisionAnswer{}
	for _, a := range resp.Answers {
		got[a.Name] = a
	}
	out := ai.Decision{Answers: map[string]ai.Answer{}, Model: resp.Model, InputTokens: resp.Usage.InputTokens, OutputTokens: resp.Usage.OutputTokens}
	for _, q := range req.Questions {
		a, ok := got[q.Name]
		if !ok {
			return ai.Decision{}, errors.Newf("ai/openai: no answer for question %q", q.Name)
		}
		answer, err := decisionAnswerOf(q, a)
		if err != nil {
			return ai.Decision{}, err
		}
		out.Answers[q.Name] = answer
	}
	return out, nil
}

// decisionInput 在没有图片的文本材料时直接发送字符串，否则发送一条用户消息：文本或 JSON，再依次是图片，命名的图片前面加上名称。
func decisionInput(req ai.DecisionRequest) any {
	text, isText := req.Input.(string)
	if isText && len(req.Images) == 0 {
		return text
	}
	var parts []map[string]any
	if isText {
		parts = append(parts, map[string]any{"type": "input_text", "text": text})
	} else {
		parts = append(parts, map[string]any{"type": "input_json", "data": req.Input})
	}
	for _, img := range req.Images {
		if img.Name != "" {
			parts = append(parts, map[string]any{"type": "input_text", "text": img.Name + ":"})
		}
		parts = append(parts, map[string]any{"type": "input_image", "image_url": DataURL(img)})
	}
	return []map[string]any{{"role": "user", "content": parts}}
}

func decisionQuestions(qs []ai.Question) []map[string]any {
	out := make([]map[string]any, len(qs))
	for i, q := range qs {
		m := map[string]any{"name": q.Name, "instructions": q.Instructions}
		switch q.Kind {
		case ai.YesNoQuestion:
			// predicate 没有判断标准的字段，写进问题里。
			m["type"] = "predicate"
			var b strings.Builder
			b.WriteString(q.Instructions)
			if q.IfTrue != "" {
				b.WriteString("\n\nTrue: " + q.IfTrue)
			}
			if q.IfFalse != "" {
				b.WriteString("\n\nFalse: " + q.IfFalse)
			}
			m["instructions"] = b.String()
		case ai.ChoiceQuestion:
			m["type"] = "choice"
			choices := make([]map[string]any, len(q.Options))
			for j, o := range q.Options {
				choices[j] = map[string]any{"value": o.Value}
				if o.Description != "" {
					choices[j]["description"] = o.Description
				}
			}
			m["choices"] = choices
		case ai.ScoreQuestion:
			m["type"] = "score"
			levels := make([]map[string]any, len(q.Levels))
			for j, l := range q.Levels {
				levels[j] = map[string]any{"label": l.Label}
				if l.Description != "" {
					levels[j]["description"] = l.Description
				}
			}
			m["levels"] = levels
		}
		out[i] = m
	}
	return out
}

func decisionAnswerOf(q ai.Question, a decisionAnswer) (ai.Answer, error) {
	if a.Type == "refusal" {
		return ai.NewAnswer(q, ai.Answer{Refused: true}), nil
	}
	want := map[ai.QuestionKind]string{ai.YesNoQuestion: "predicate", ai.ChoiceQuestion: "choice", ai.ScoreQuestion: "score"}[q.Kind]
	if a.Type != want {
		return ai.Answer{}, errors.Newf("ai/openai: question %q answered as %q, want %q", q.Name, a.Type, want)
	}
	out := ai.Answer{Probability: a.Probability, Choice: a.Choice, Score: a.Score, Confidence: a.Confidence}
	switch q.Kind {
	case ai.ChoiceQuestion:
		byValue := map[string]float64{}
		for _, p := range a.Probabilities {
			var v string
			if err := json.Unmarshal(p.Value, &v); err != nil {
				return ai.Answer{}, errors.Wrapf(err, "ai/openai: question %q", q.Name)
			}
			byValue[v] = p.Probability
		}
		out.Probabilities = make([]float64, len(q.Options))
		for i, o := range q.Options {
			out.Probabilities[i] = byValue[o.Value]
		}
	case ai.ScoreQuestion:
		out.Probabilities = make([]float64, len(q.Levels))
		for _, p := range a.Probabilities {
			i, err := strconv.Atoi(strings.Trim(string(p.Value), `"`))
			if err != nil || i < 0 || i >= len(q.Levels) {
				return ai.Answer{}, errors.Newf("ai/openai: question %q has unknown level %s", q.Name, p.Value)
			}
			out.Probabilities[i] = p.Probability
		}
	}
	return ai.NewAnswer(q, out), nil
}
