package ai_test

import (
	"testing"

	"github.com/ryan053857/mygoravel/ai"
)

func TestModelConfigValidate(t *testing.T) {
	if err := (ai.ModelConfig{}).Validate(); err == nil {
		t.Fatal("empty config should be invalid")
	}
	if err := (ai.ModelConfig{Model: "deepseek-chat", APIKey: "k"}).Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestModelConfigRetries(t *testing.T) {
	for in, want := range map[int]int{0: 2, -1: 0, -5: 0, 5: 5} {
		if got := (ai.ModelConfig{MaxRetries: in}).Retries(); got != want {
			t.Errorf("Retries() with %d = %d, want %d", in, got, want)
		}
	}
}
