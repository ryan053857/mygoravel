package chat_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/model"

	"github.com/go-tenon/tenon/ai"
	"github.com/go-tenon/tenon/ai/chat"
)

// failingServer 总是返回 500，并让客户端几乎不等待就重试；返回请求计数。
func failingServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.Header().Set("Retry-After-Ms", "1")
		http.Error(w, `{"error":{"message":"overloaded"}}`, http.StatusInternalServerError)
	}))
	t.Cleanup(s.Close)
	return s, &hits
}

func TestModelRetries(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		maxRetries int
		requests   int32
	}{
		{0, 3},  // 默认重试 2 次
		{-1, 1}, // 不重试
		{4, 5},
	} {
		s, hits := failingServer(t)
		m := chat.NewModel(ai.ModelConfig{Model: "m", BaseURL: s.URL, APIKey: "k", MaxRetries: tc.maxRetries})
		ch, err := m.GenerateContent(context.Background(), &model.Request{
			Messages:         []model.Message{model.NewUserMessage("hi")},
			GenerationConfig: model.GenerationConfig{Stream: false},
		})
		failed := err != nil
		if err == nil {
			for rsp := range ch {
				if rsp.Error != nil {
					failed = true
				}
			}
		}
		if !failed {
			t.Errorf("max_retries %d: expected an error", tc.maxRetries)
		}
		if got := hits.Load(); got != tc.requests {
			t.Errorf("max_retries %d: %d requests, want %d", tc.maxRetries, got, tc.requests)
		}
	}
}
