package systemone_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/openrouter"
	"github.com/ryan053857/mygoravel/ai/systemone"
)

type recorded struct {
	path string
	body string
}

func server(t *testing.T, response string) (string, *[]recorded) {
	t.Helper()
	var got []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer sk-test", r.Header.Get("Authorization"))
		body, _ := io.ReadAll(r.Body)
		got = append(got, recorded{r.URL.Path, string(body)})
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, response)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &got
}

var questions = []ai.Question{
	ai.Choice("department", "由谁处理？", ai.ChoiceOption{Value: "technical", Description: "产品故障"}, ai.ChoiceOption{Value: "billing"}),
	ai.YesNo("escalate", "是否通知值班？").Criteria("核心功能不可用", ""),
	ai.YesNo("spam", "是否垃圾信息？"),
	ai.Score("severity", "有多严重？", ai.Level{Label: "轻微", Description: "不影响功能"}, ai.Level{Label: "严重"}),
}

const answers = `{
  "model": "decision-model-preview",
  "answers": {
    "department": {"type": "choice", "choice": "billing", "confidence": 0.63, "probabilities": {"billing": 0.82, "technical": 0.18}},
    "escalate": {"type": "noul", "noul": 0.99},
    "spam": {"type": "refusal"},
    "severity": {"type": "score", "score": 0.69, "legend": {"0": "轻微", "1": "严重"}, "probabilities": {"0": 0.31, "1": 0.69}, "confidence": 0.88}
  },
  "usage": {"input_tokens": 111}
}`

func TestDecide(t *testing.T) {
	url, got := server(t, answers)
	c := systemone.New(ai.ModelConfig{Model: "decision-model-preview", BaseURL: url + "/compatible-mode/v1", APIKey: "sk-test"})

	res, err := c.Decide(t.Context(), ai.DecisionRequest{Input: map[string]any{"content": "支付后未到账"}, Questions: questions})
	require.NoError(t, err)
	assert.Equal(t, "decision-model-preview", res.Model)
	assert.Equal(t, 111, res.InputTokens)
	assert.Equal(t, "billing", res.Answers["department"].Choice)
	assert.Equal(t, []float64{0.18, 0.82}, res.Answers["department"].Probabilities, "按选项顺序")
	assert.True(t, res.Answers["escalate"].Yes(0.9))
	assert.True(t, res.Answers["spam"].Refused)
	assert.Equal(t, "严重", res.Answers["severity"].Label())
	assert.Equal(t, 0.88, res.Answers["severity"].Confidence)

	require.Len(t, *got, 1)
	assert.Equal(t, "/compatible-mode/v1/systemone", (*got)[0].path)
	// 选项按传入的顺序编码，technical 在 billing 之前。
	assert.JSONEq(t, `{
	  "model": "decision-model-preview",
	  "state": {"content": "支付后未到账"},
	  "questions": {
	    "department": {"type": "choice", "instructions": "由谁处理？", "criteria": {"technical": "产品故障", "billing": "billing"}},
	    "escalate": {"type": "noul", "instructions": "是否通知值班？", "criteria": {"true": "核心功能不可用", "false": ""}},
	    "spam": {"type": "noul", "instructions": "是否垃圾信息？"},
	    "severity": {"type": "score", "instructions": "有多严重？", "criteria": ["轻微：不影响功能", "严重"]}
	  }
	}`, (*got)[0].body)
	assert.Contains(t, (*got)[0].body, `"criteria":{"technical":"产品故障","billing":"billing"}`)
}

func TestDecideImages(t *testing.T) {
	url, got := server(t, `{"answers": {"damaged": {"type": "noul", "noul": 0.1}}}`)
	c := systemone.New(ai.ModelConfig{Model: "m", BaseURL: url, APIKey: "sk-test"})

	_, err := c.Decide(t.Context(), ai.DecisionRequest{
		Input:     "检查商品照片。",
		Images:    []ai.Media{{Data: []byte("a"), MIME: "image/png", Name: "front"}, {Data: []byte("b"), MIME: "image/jpeg"}},
		Questions: []ai.Question{ai.YesNo("damaged", "front 是否破损？")},
	})
	require.NoError(t, err)
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte((*got)[0].body), &body))
	var want any
	require.NoError(t, json.Unmarshal([]byte(`["检查商品照片。", "front:",
	  {"type": "image_url", "image_url": {"url": "data:image/png;base64,YQ=="}},
	  {"type": "image_url", "image_url": {"url": "data:image/jpeg;base64,Yg=="}}]`), &want))
	assert.Equal(t, want, body["state"])
}

func TestDecideErrors(t *testing.T) {
	url, _ := server(t, `{"answers": {"department": {"type": "noul", "noul": 1}}}`)
	c := systemone.New(ai.ModelConfig{Model: "m", BaseURL: url, APIKey: "sk-test"})

	_, err := c.Decide(t.Context(), ai.DecisionRequest{Input: "x", Questions: questions[:1]})
	assert.ErrorContains(t, err, `answered as "noul", want "choice"`)
	_, err = c.Decide(t.Context(), ai.DecisionRequest{Input: "x", Questions: questions[1:2]})
	assert.ErrorContains(t, err, `no answer for question "escalate"`)
	_, err = c.Decide(t.Context(), ai.DecisionRequest{Questions: questions})
	assert.ErrorContains(t, err, "input is required")
}

func TestOpenRouterDecideUsesSystemOne(t *testing.T) {
	url, got := server(t, answers)
	c := openrouter.New(ai.ModelConfig{Model: "typesafe/jev-1.14", BaseURL: url + "/api/v1", APIKey: "sk-test"})

	res, err := c.Decide(t.Context(), ai.DecisionRequest{Input: "x", Questions: questions})
	require.NoError(t, err)
	assert.Equal(t, "billing", res.Answers["department"].Choice)
	assert.Equal(t, "/api/v1/systemone", (*got)[0].path)
}
