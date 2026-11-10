package ai_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/log"

	"github.com/ryan053857/mygoravel/ai"
)

func TestSetLogger(t *testing.T) {
	oldDefault, oldContext := log.Default, log.ContextDefault
	t.Cleanup(func() { log.Default, log.ContextDefault = oldDefault, oldContext })

	var buf bytes.Buffer
	ai.SetLogger(slog.New(slog.NewTextHandler(&buf, nil)))
	log.Warnf("model %s slow", "x")
	log.Debug("hidden")
	got := buf.String()
	if !strings.Contains(got, "level=WARN") || !strings.Contains(got, `msg="model x slow"`) || !strings.Contains(got, "component=ai") {
		t.Fatalf("log output %q", got)
	}
	if strings.Contains(got, "hidden") {
		t.Fatalf("debug should follow the handler level: %q", got)
	}
}

func TestModelConfigValidate(t *testing.T) {
	if err := (ai.ModelConfig{}).Validate(); err == nil {
		t.Fatal("empty config should be invalid")
	}
	if err := (ai.ModelConfig{Model: "deepseek-chat", APIKey: "k"}).Validate(); err != nil {
		t.Fatal(err)
	}
}
