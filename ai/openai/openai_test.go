package openai_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ryan053857/mygoravel/ai"
	"github.com/ryan053857/mygoravel/ai/openai"
)

// pngData 是一张 1x1 的 PNG。
var pngData, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==")

type request struct {
	Path  string
	Auth  string
	JSON  map[string]any
	Form  map[string]string
	Files map[string]string // 字段名 -> 文件名
}

// server 记录收到的请求，并用 handle 给出响应。
type server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []request
}

func newServer(t *testing.T, handle func(w http.ResponseWriter, r request)) *server {
	t.Helper()
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := request{Path: r.URL.Path, Auth: r.Header.Get("Authorization")}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			require.NoError(t, r.ParseMultipartForm(1<<20))
			req.Form, req.Files = map[string]string{}, map[string]string{}
			for k, v := range r.MultipartForm.Value {
				req.Form[k] = v[0]
			}
			for k, fs := range r.MultipartForm.File {
				names := make([]string, len(fs))
				for i, f := range fs {
					names[i] = f.Filename + "|" + f.Header.Get("Content-Type")
				}
				req.Files[k] = strings.Join(names, ",")
			}
		} else if r.Body != nil {
			body, _ := io.ReadAll(r.Body)
			if len(body) > 0 {
				require.NoError(t, json.Unmarshal(body, &req.JSON))
			}
		}
		s.mu.Lock()
		s.requests = append(s.requests, req)
		s.mu.Unlock()
		handle(w, req)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) last() request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests[len(s.requests)-1]
}

func (s *server) client(model string, opts ...openai.Option) *openai.Client {
	return openai.New(ai.ModelConfig{Model: model, BaseURL: s.URL + "/v1", APIKey: "sk-test"}, opts...)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func TestImage(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(pngData)}}})
	})

	img, err := s.client("gpt-image-1").Image(t.Context(), ai.ImageRequest{Prompt: "一只猫", Aspect: ai.Landscape, Quality: "high"})
	require.NoError(t, err)
	assert.Equal(t, ai.Media{Data: pngData, MIME: "image/png"}, img)

	r := s.last()
	assert.Equal(t, "/v1/images/generations", r.Path)
	assert.Equal(t, "Bearer sk-test", r.Auth)
	assert.Equal(t, map[string]any{"prompt": "一只猫", "model": "gpt-image-1", "size": "1536x1024", "quality": "high"}, r.JSON)
}

func TestImageDallE3(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(pngData)}}})
	})

	_, err := s.client("dall-e-3").Image(t.Context(), ai.ImageRequest{Prompt: "猫", Aspect: ai.Portrait, Quality: "high"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"prompt": "猫", "model": "dall-e-3", "size": "1024x1792", "quality": "hd", "response_format": "b64_json"}, s.last().JSON)
}

func TestImageURLAndCustomSizes(t *testing.T) {
	var s *server
	s = newServer(t, func(w http.ResponseWriter, r request) {
		if r.Path == "/files/cat.png" {
			_, _ = w.Write(pngData)
			return
		}
		writeJSON(w, map[string]any{"data": []map[string]any{{"url": s.URL + "/files/cat.png"}}})
	})

	img, err := s.client("doubao-seedream", openai.WithImageSizes(map[ai.Aspect]string{ai.Square: "2048x2048"})).
		Image(t.Context(), ai.ImageRequest{Prompt: "猫", Aspect: ai.Square})
	require.NoError(t, err)
	assert.Equal(t, ai.Media{Data: pngData, MIME: "image/png"}, img)
	assert.Equal(t, "2048x2048", s.requests[0].JSON["size"])
}

func TestImageEdit(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, map[string]any{"data": []map[string]any{{"b64_json": base64.StdEncoding.EncodeToString(pngData)}}})
	})

	refs := []ai.Media{{Data: pngData, MIME: "image/png"}, {Data: []byte("jpeg"), MIME: "image/jpeg"}}
	_, err := s.client("gpt-image-1").Image(t.Context(), ai.ImageRequest{Prompt: "合成两张图", References: refs})
	require.NoError(t, err)

	r := s.last()
	assert.Equal(t, "/v1/images/edits", r.Path)
	assert.Equal(t, map[string]string{"prompt": "合成两张图", "model": "gpt-image-1"}, r.Form)
	assert.Equal(t, map[string]string{"image[]": "image-1.png|image/png,image-2.jpg|image/jpeg"}, r.Files)
}

func TestImageErrors(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		w.WriteHeader(http.StatusBadRequest)
		writeJSON(w, map[string]any{"error": map[string]any{"message": "prompt rejected"}})
	})
	c := s.client("gpt-image-1")

	_, err := c.Image(t.Context(), ai.ImageRequest{})
	assert.ErrorContains(t, err, "prompt is required")
	_, err = c.Image(t.Context(), ai.ImageRequest{Prompt: "猫", Aspect: "wide"})
	assert.ErrorContains(t, err, `unknown aspect "wide"`)
	_, err = c.Image(t.Context(), ai.ImageRequest{Prompt: "猫"})
	assert.ErrorContains(t, err, "prompt rejected")
}

func TestSpeak(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		if r.JSON["response_format"] == "wav" {
			w.Header().Set("Content-Type", "audio/wav")
		}
		_, _ = w.Write([]byte("audio"))
	})
	c := s.client("gpt-4o-mini-tts")

	audio, err := c.Speak(t.Context(), ai.SpeechRequest{Text: "你好", Voice: "alloy", Instructions: "轻快"})
	require.NoError(t, err)
	assert.Equal(t, ai.Media{Data: []byte("audio"), MIME: "audio/mpeg"}, audio)
	r := s.last()
	assert.Equal(t, "/v1/audio/speech", r.Path)
	assert.Equal(t, map[string]any{"input": "你好", "model": "gpt-4o-mini-tts", "voice": "alloy", "instructions": "轻快"}, r.JSON)

	audio, err = c.Speak(t.Context(), ai.SpeechRequest{Text: "你好", Voice: "alloy", Format: "wav"})
	require.NoError(t, err)
	assert.Equal(t, "audio/wav", audio.MIME)

	_, err = c.Speak(t.Context(), ai.SpeechRequest{Text: "你好"})
	assert.ErrorContains(t, err, "voice is required")
}

func TestTranscribe(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, map[string]any{"text": "你好世界"})
	})

	text, err := s.client("whisper-1").Transcribe(t.Context(), ai.TranscriptionRequest{
		Audio:    ai.Media{Data: []byte("audio"), MIME: "audio/webm;codecs=opus"},
		Language: "zh",
		Prompt:   "mygoravel",
	})
	require.NoError(t, err)
	assert.Equal(t, "你好世界", text)

	r := s.last()
	assert.Equal(t, "/v1/audio/transcriptions", r.Path)
	assert.Equal(t, map[string]string{"model": "whisper-1", "language": "zh", "prompt": "mygoravel"}, r.Form)
	assert.Equal(t, map[string]string{"file": "audio.webm|audio/webm;codecs=opus"}, r.Files)

	_, err = s.client("whisper-1").Transcribe(t.Context(), ai.TranscriptionRequest{})
	assert.ErrorContains(t, err, "audio is required")
}

func TestEmbed(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		input := r.JSON["input"].([]any)
		// 倒序返回，检查按 index 对应回原文。
		data := []map[string]any{}
		for i := len(input) - 1; i >= 0; i-- {
			data = append(data, map[string]any{"index": i, "embedding": []float64{float64(len(input[i].(string))), 0.5}})
		}
		writeJSON(w, map[string]any{"data": data})
	})

	vecs, err := s.client("text-embedding-3-small", openai.WithDimensions(2), openai.WithBatchSize(2)).
		Embed(t.Context(), []string{"a", "bb", "ccc"})
	require.NoError(t, err)
	assert.Equal(t, [][]float32{{1, 0.5}, {2, 0.5}, {3, 0.5}}, vecs)

	require.Len(t, s.requests, 2)
	assert.Equal(t, map[string]any{"input": []any{"a", "bb"}, "model": "text-embedding-3-small", "dimensions": float64(2), "encoding_format": "float"}, s.requests[0].JSON)
	assert.Equal(t, []any{"ccc"}, s.requests[1].JSON["input"])

	vecs, err = s.client("m").Embed(t.Context(), nil)
	require.NoError(t, err)
	assert.Empty(t, vecs)
	assert.Len(t, s.requests, 2)
}

func TestEmbedMissing(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		writeJSON(w, map[string]any{"data": []map[string]any{{"index": 0, "embedding": []float64{1}}}})
	})
	_, err := s.client("m").Embed(t.Context(), []string{"a", "b"})
	assert.ErrorContains(t, err, "no embedding returned for text 1")
}

func ExampleNew() {
	img := openai.New(ai.ModelConfig{Model: "gpt-image-1", APIKey: "sk-..."})
	fmt.Printf("%T\n", ai.Imager(img))
	// Output: *openai.Client
}

func TestRerank(t *testing.T) {
	s := newServer(t, func(w http.ResponseWriter, r request) {
		// 故意乱序返回，检查按分数排序。
		writeJSON(w, map[string]any{"results": []map[string]any{
			{"index": 0, "relevance_score": 0.2},
			{"index": 1, "relevance_score": 0.9},
		}})
	})
	c := s.client("bge-reranker")

	got, err := c.Rerank(t.Context(), "PHP 框架", []string{"Django", "Laravel", "React"}, 2)
	require.NoError(t, err)
	assert.Equal(t, []ai.Ranked{{Index: 1, Score: 0.9}, {Index: 0, Score: 0.2}}, got)
	r := s.last()
	assert.Equal(t, "/v1/rerank", r.Path)
	assert.Equal(t, map[string]any{"model": "bge-reranker", "query": "PHP 框架", "documents": []any{"Django", "Laravel", "React"}, "top_n": float64(2)}, r.JSON)

	// limit 不小于文档数时不发 top_n。
	_, err = c.Rerank(t.Context(), "q", []string{"a", "b"}, 5)
	require.NoError(t, err)
	assert.NotContains(t, s.last().JSON, "top_n")

	got, err = c.Rerank(t.Context(), "q", nil, 1)
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Len(t, s.requests, 2)

	_, err = c.Rerank(t.Context(), "q", []string{"a"}, 0)
	assert.ErrorContains(t, err, "rerank index 1 out of range")
}

func TestClientRetries(t *testing.T) {
	for _, tc := range []struct {
		maxRetries int
		requests   int32
	}{{0, 3}, {-1, 1}} {
		var hits atomic.Int32
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("Retry-After-Ms", "1")
			http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusTooManyRequests)
		}))
		c := openai.New(ai.ModelConfig{Model: "m", BaseURL: s.URL, APIKey: "k", MaxRetries: tc.maxRetries})
		if _, err := c.Embed(context.Background(), []string{"x"}); err == nil {
			t.Errorf("max_retries %d: expected an error", tc.maxRetries)
		}
		s.Close()
		if got := hits.Load(); got != tc.requests {
			t.Errorf("max_retries %d: %d requests, want %d", tc.maxRetries, got, tc.requests)
		}
	}
}
