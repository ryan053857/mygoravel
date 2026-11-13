package openrouter_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/go-tenon/tenon/ai"
	"github.com/go-tenon/tenon/ai/openrouter"
)

var pngData, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

type request struct {
	Path string
	Body map[string]any
}

func newClient(t *testing.T, respond func(r request) any) (*openrouter.Client, *[]request) {
	t.Helper()
	var got []request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer sk-or", r.Header.Get("Authorization"))
		req := request{Path: r.URL.Path}
		body, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(body, &req.Body))
		got = append(got, req)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(respond(req))
	}))
	t.Cleanup(srv.Close)
	return openrouter.New(ai.ModelConfig{Model: "vendor/model", BaseURL: srv.URL + "/api/v1", APIKey: "sk-or"}), &got
}

func TestImage(t *testing.T) {
	c, got := newClient(t, func(request) any {
		return map[string]any{"created": 0, "data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString([]byte("jpeg")), "media_type": "image/jpeg"}}}
	})

	img, err := c.Image(t.Context(), ai.ImageRequest{Prompt: "猫", Aspect: ai.Landscape, Quality: "low"})
	require.NoError(t, err)
	assert.Equal(t, ai.Media{Data: []byte("jpeg"), MIME: "image/jpeg"}, img)
	assert.Equal(t, request{Path: "/api/v1/images", Body: map[string]any{
		"model": "vendor/model", "prompt": "猫", "aspect_ratio": "3:2", "quality": "low",
	}}, (*got)[0])
}

func TestImageReferences(t *testing.T) {
	c, got := newClient(t, func(request) any {
		return map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(pngData)}}}
	})

	img, err := c.Image(t.Context(), ai.ImageRequest{Prompt: "换成水彩风格", References: []ai.Media{{Data: pngData, MIME: "image/png"}}})
	require.NoError(t, err)
	assert.Equal(t, ai.Media{Data: pngData, MIME: "image/png"}, img) // 没有 media_type 时从内容识别
	assert.Equal(t, []any{map[string]any{
		"type":      "image_url",
		"image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(pngData)},
	}}, (*got)[0].Body["input_references"])
}

func TestImageErrors(t *testing.T) {
	c, _ := newClient(t, func(request) any { return map[string]any{"data": []any{}} })

	_, err := c.Image(t.Context(), ai.ImageRequest{})
	assert.ErrorContains(t, err, "prompt is required")
	_, err = c.Image(t.Context(), ai.ImageRequest{Prompt: "猫", Aspect: "wide"})
	assert.ErrorContains(t, err, `unknown aspect "wide"`)
	_, err = c.Image(t.Context(), ai.ImageRequest{Prompt: "猫"})
	assert.ErrorContains(t, err, "no image returned")
}

func TestEmbedUsesCompatibleAPI(t *testing.T) {
	c, got := newClient(t, func(request) any {
		return map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float64{1, 0}}}}
	})

	vecs, err := c.Embed(t.Context(), []string{"猫"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0}}, vecs)
	assert.Equal(t, "/api/v1/embeddings", (*got)[0].Path)
}
