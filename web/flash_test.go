package web_test

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/go-tenon/tenon/web"
)

// flashTrigger 解析 HX-Trigger 中的提示事件。
func flashTrigger(t *testing.T, rr *httptest.ResponseRecorder) []web.Message {
	t.Helper()
	raw := rr.Header().Get("HX-Trigger")
	if raw == "" {
		return nil
	}
	for _, r := range raw {
		if r > 127 {
			t.Fatalf("HX-Trigger is not ASCII: %s", raw)
		}
	}
	var events map[string]struct {
		Target   string        `json:"target"`
		Messages []web.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		t.Fatalf("HX-Trigger %q: %v", raw, err)
	}
	ev, ok := events[web.FlashEvent]
	if ok && ev.Target != "body" {
		t.Errorf("flash event target = %q", ev.Target)
	}
	return ev.Messages
}
