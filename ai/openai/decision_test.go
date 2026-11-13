package openai_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-tenon/tenon/ai"
)

var ticketQuestions = []ai.Question{
	ai.YesNo("urgent", "是否紧急？").Criteria("核心功能不可用", "可以等"),
	ai.Choice("department", "由谁处理？", ai.ChoiceOption{Value: "billing", Description: "付款与退款"}, ai.ChoiceOption{Value: "technical"}),
	ai.Score("severity", "有多严重？", ai.Level{Label: "轻微", Description: "只影响外观"}, ai.Level{Label: "阻塞"}),
}

func decisionJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &m))
	return m
}

func TestDecide(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, decisionJSON(t, `{
		  "model": "gpt-6-luna",
		  "answers": [
		    {"type": "score", "name": "severity", "score": 0.7,
		     "probabilities": [{"value": 0, "label": "轻微", "probability": 0.3}, {"value": 1, "label": "阻塞", "probability": 0.7}], "confidence": 0.5},
		    {"type": "predicate", "name": "urgent", "probability": 0.92},
		    {"type": "choice", "name": "department", "choice": "billing",
		     "probabilities": [{"value": "billing", "probability": 0.95}, {"value": "technical", "probability": 0.05}], "confidence": 0.93}
		  ],
		  "usage": {"input_tokens": 120, "output_tokens": 0}
		}`))
	})

	res, err := s.client("gpt-6-luna").Decide(t.Context(), ai.DecisionRequest{Input: "我被重复扣款了", Questions: ticketQuestions})
	require.NoError(t, err)
	assert.Equal(t, "gpt-6-luna", res.Model)
	assert.Equal(t, 120, res.InputTokens)
	assert.True(t, res.Answers["urgent"].Yes(0.9))
	assert.Equal(t, "billing", res.Answers["department"].Choice)
	assert.Equal(t, []float64{0.95, 0.05}, res.Answers["department"].Probabilities)
	assert.Equal(t, 0.93, res.Answers["department"].Confidence)
	assert.Equal(t, 0.7, res.Answers["severity"].Score)
	assert.Equal(t, "阻塞", res.Answers["severity"].Label())

	r := s.last()
	assert.Equal(t, "/v1/decisions", r.Path)
	assert.Equal(t, decisionJSON(t, `{
	  "model": "gpt-6-luna",
	  "input": "我被重复扣款了",
	  "questions": [
	    {"type": "predicate", "name": "urgent", "instructions": "是否紧急？\n\nTrue: 核心功能不可用\n\nFalse: 可以等"},
	    {"type": "choice", "name": "department", "instructions": "由谁处理？",
	     "choices": [{"value": "billing", "description": "付款与退款"}, {"value": "technical"}]},
	    {"type": "score", "name": "severity", "instructions": "有多严重？",
	     "levels": [{"label": "轻微", "description": "只影响外观"}, {"label": "阻塞"}]}
	  ]
	}`), r.JSON)
}

func TestDecideStructuredInputAndImages(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, decisionJSON(t, `{"answers": [{"type": "refusal", "name": "damaged"}]}`))
	})

	res, err := s.client("gpt-6-luna").Decide(t.Context(), ai.DecisionRequest{
		Input:     map[string]any{"order": map[string]any{"charges": []int{49, 49}}},
		Images:    []ai.Media{{Data: []byte("a"), MIME: "image/png", Name: "front"}, {Data: []byte("b"), MIME: "image/jpeg"}},
		Questions: []ai.Question{ai.YesNo("damaged", "front 中的商品是否损坏？")},
	})
	require.NoError(t, err)
	assert.True(t, res.Answers["damaged"].Refused)
	assert.False(t, res.Answers["damaged"].Yes(0))

	var want any
	require.NoError(t, json.Unmarshal([]byte(`[{"role": "user", "content": [
	  {"type": "input_json", "data": {"order": {"charges": [49, 49]}}},
	  {"type": "input_text", "text": "front:"},
	  {"type": "input_image", "image_url": "data:image/png;base64,YQ=="},
	  {"type": "input_image", "image_url": "data:image/jpeg;base64,Yg=="}
	]}]`), &want))
	assert.Equal(t, want, s.last().JSON["input"])
}

func TestDecideErrors(t *testing.T) {
	answers := `{"answers": []}`
	s := newServer(t, func(w http.ResponseWriter, r request) { writeJSON(w, decisionJSON(t, answers)) })
	c := s.client("gpt-6-luna")

	_, err := c.Decide(t.Context(), ai.DecisionRequest{Input: "x"})
	assert.ErrorContains(t, err, "at least one question")

	_, err = c.Decide(t.Context(), ai.DecisionRequest{Input: "x", Questions: ticketQuestions[:1]})
	assert.ErrorContains(t, err, `no answer for question "urgent"`)

	answers = `{"answers": [{"type": "choice", "name": "urgent", "choice": "x"}]}`
	_, err = c.Decide(t.Context(), ai.DecisionRequest{Input: "x", Questions: ticketQuestions[:1]})
	assert.ErrorContains(t, err, `answered as "choice", want "predicate"`)
}
