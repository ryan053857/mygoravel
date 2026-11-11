package aitest_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/aitest"
)

func TestImager(t *testing.T) {
	img := aitest.NewImager()
	got, err := img.Image(t.Context(), ai.ImageRequest{Prompt: "猫", Aspect: ai.Square})
	require.NoError(t, err)
	assert.Equal(t, aitest.PNG, got)
	assert.Equal(t, []ai.ImageRequest{{Prompt: "猫", Aspect: ai.Square}}, img.Requests())

	jpeg := ai.Media{Data: []byte("jpeg"), MIME: "image/jpeg"}
	img = aitest.NewImager(jpeg)
	got, err = img.Image(t.Context(), ai.ImageRequest{Prompt: "a"})
	require.NoError(t, err)
	assert.Equal(t, jpeg, got)
	_, err = img.Image(t.Context(), ai.ImageRequest{Prompt: "b"})
	assert.ErrorContains(t, err, "unexpected image request 2, script has 1 results")

	boom := errors.New("boom")
	img.FailWith(boom)
	_, err = img.Image(t.Context(), ai.ImageRequest{Prompt: "c"})
	assert.ErrorIs(t, err, boom)
	assert.Len(t, img.Requests(), 3)
}

func TestSpeakerAndTranscriber(t *testing.T) {
	sp := aitest.NewSpeaker()
	audio, err := sp.Speak(t.Context(), ai.SpeechRequest{Text: "你好", Voice: "alloy"})
	require.NoError(t, err)
	assert.Equal(t, ai.Media{Data: []byte("你好"), MIME: "audio/mpeg"}, audio)

	// 默认的 Speaker 和 Transcriber 首尾相接，文字原样往返。
	tr := aitest.NewTranscriber()
	text, err := tr.Transcribe(t.Context(), ai.TranscriptionRequest{Audio: audio})
	require.NoError(t, err)
	assert.Equal(t, "你好", text)

	tr = aitest.NewTranscriber("预设")
	text, err = tr.Transcribe(t.Context(), ai.TranscriptionRequest{Audio: audio, Language: "zh"})
	require.NoError(t, err)
	assert.Equal(t, "预设", text)
	assert.Equal(t, "zh", tr.Requests()[0].Language)
}

func TestEmbedder(t *testing.T) {
	e := aitest.NewEmbedder(16)
	vecs, err := e.Embed(t.Context(), []string{"猫", "狗", "猫"})
	require.NoError(t, err)
	require.Len(t, vecs, 3)
	assert.Len(t, vecs[0], 16)
	assert.Equal(t, vecs[0], vecs[2])
	assert.Equal(t, aitest.Vector("狗", 16), vecs[1])
	assert.InDelta(t, 1, dot(vecs[0], vecs[0]), 1e-5)
	assert.Less(t, dot(vecs[0], vecs[1]), float32(0.9))
	assert.Equal(t, [][]string{{"猫", "狗", "猫"}}, e.Requests())
}

func dot(a, b []float32) float32 {
	var s float32
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}
