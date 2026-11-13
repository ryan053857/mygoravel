package ai_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-tenon/tenon/ai"
	"github.com/go-tenon/tenon/ai/aitest"
)

func TestDecisionRequestValidate(t *testing.T) {
	yes := ai.YesNo("a", "?")
	cases := map[string]ai.DecisionRequest{
		"decision input is required":       {Questions: []ai.Question{yes}},
		"at least one question":            {Input: "x"},
		"question name is required":        {Input: "x", Questions: []ai.Question{ai.YesNo("", "?")}},
		`duplicate decision question "a"`:  {Input: "x", Questions: []ai.Question{yes, yes}},
		"needs at least 2 options":         {Input: "x", Questions: []ai.Question{ai.Choice("c", "?", ai.ChoiceOption{Value: "x"})}},
		"has an empty or duplicate option": {Input: "x", Questions: []ai.Question{ai.Choice("c", "?", ai.ChoiceOption{Value: "x"}, ai.ChoiceOption{Value: "x"})}},
		"needs at least 2 levels":          {Input: "x", Questions: []ai.Question{ai.Score("s", "?", ai.Levels("low")...)}},
		`unknown kind "x"`:                 {Input: "x", Questions: []ai.Question{{Name: "q", Kind: "x"}}},
	}
	for want, req := range cases {
		assert.ErrorContains(t, req.Validate(), want)
	}
	assert.NoError(t, ai.DecisionRequest{Input: map[string]int{"a": 1}, Questions: []ai.Question{yes}}.Validate())
}

func TestAnswerHelpers(t *testing.T) {
	choice := ai.Choice("c", "?", ai.ChoiceOption{Value: "a"}, ai.ChoiceOption{Value: "b"})
	a := ai.NewAnswer(choice, ai.Answer{Choice: "b", Probabilities: []float64{0.3, 0.7}})
	assert.Equal(t, ai.ChoiceQuestion, a.Kind)
	assert.Equal(t, 0.7, a.ProbabilityOf("b"))
	assert.Equal(t, 0.0, a.ProbabilityOf("z"))

	score := ai.Score("s", "?", ai.Levels("低", "中", "高")...)
	a = ai.NewAnswer(score, ai.Answer{Score: 1.2, Probabilities: []float64{0.1, 0.6, 0.3}})
	assert.Equal(t, 1, a.Level())
	assert.Equal(t, "中", a.Label())

	yes := ai.NewAnswer(ai.YesNo("y", "?"), ai.Answer{Probability: 0.8})
	assert.True(t, yes.Yes(0.8))
	assert.False(t, yes.Yes(0.81))

	refused := ai.NewAnswer(score, ai.Answer{Refused: true})
	assert.Equal(t, -1, refused.Level())
	assert.Empty(t, refused.Label())
	assert.False(t, ai.NewAnswer(ai.YesNo("y", "?"), ai.Answer{Refused: true, Probability: 1}).Yes(0.5))
}

func TestDecideBy(t *testing.T) {
	type team struct{ Slug, Scope string }
	teams := []team{{"payments", "支付"}, {"infra", "服务器"}}
	option := func(t team) ai.ChoiceOption { return ai.ChoiceOption{Value: t.Slug, Description: t.Scope} }

	d := aitest.NewDecider(
		map[string]ai.Answer{"choice": {Choice: "infra", Probabilities: []float64{0.2, 0.8}}},
		map[string]ai.Answer{"choice": {Choice: "infra", Probabilities: []float64{0.4, 0.6}}},
		map[string]ai.Answer{"choice": {Refused: true}},
		map[string]ai.Answer{"choice": {Choice: "unknown"}},
	)
	got, ok, err := ai.DecideBy(t.Context(), d, "谁负责？", "服务器宕机", teams, option, 0.7)
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, teams[1], got)

	req := d.Requests()[0]
	assert.Equal(t, "服务器宕机", req.Input)
	assert.Equal(t, []ai.Question{ai.Choice("choice", "谁负责？", option(teams[0]), option(teams[1]))}, req.Questions)

	_, ok, err = ai.DecideBy(t.Context(), d, "谁负责？", "x", teams, option, 0.7)
	require.NoError(t, err)
	assert.False(t, ok, "低于阈值")
	_, ok, err = ai.DecideBy(t.Context(), d, "谁负责？", "x", teams, option, 0)
	require.NoError(t, err)
	assert.False(t, ok, "拒答")
	_, _, err = ai.DecideBy(t.Context(), d, "谁负责？", "x", teams, option, 0)
	assert.ErrorContains(t, err, `unknown option "unknown"`)
}
