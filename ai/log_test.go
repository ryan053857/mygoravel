package ai_test

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go/log"

	"github.com/ryan053857/mygoravel/ai"
)

// trpcLog 收集 trpc-agent-go 的日志。SetLogger 改的是进程级全局变量，trpc-agent-go
// 内部的 goroutine 会在 Run 结束后继续读它，所以只在 TestMain 中、任何测试开始之前设置一次。
var trpcLog lockedBuffer

func TestMain(m *testing.M) {
	ai.SetLogger(slog.New(slog.NewTextHandler(&trpcLog, nil)))
	os.Exit(m.Run())
}

func TestSetLogger(t *testing.T) {
	log.Warnf("model %s slow", "x")
	log.Debug("hidden by handler level")
	got := trpcLog.String()
	if !strings.Contains(got, `level=WARN msg="model x slow" component=ai`) {
		t.Fatalf("log output %q", got)
	}
	if strings.Contains(got, "hidden by handler level") {
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

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
