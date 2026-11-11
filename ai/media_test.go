package ai_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
)

func TestRerankBy(t *testing.T) {
	type post struct{ Title string }
	posts := []post{{"Django 入门"}, {"Laravel 与 PHP"}, {"React Hooks"}}

	got, err := ai.RerankBy(t.Context(), aitest.NewReranker(), "PHP", posts, func(p post) string { return p.Title }, 1)
	require.NoError(t, err)
	assert.Equal(t, []post{{"Laravel 与 PHP"}}, got)

	got, err = ai.RerankBy(t.Context(), aitest.NewReranker(), "PHP", nil, func(p post) string { return p.Title }, 1)
	require.NoError(t, err)
	assert.Empty(t, got)

	_, err = ai.RerankBy(t.Context(), aitest.NewReranker([]ai.Ranked{{Index: 5}}), "PHP", posts, func(p post) string { return p.Title }, 0)
	assert.ErrorContains(t, err, "rerank index 5 out of range")
}
