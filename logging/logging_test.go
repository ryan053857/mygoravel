package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/logging"
)

func TestNewAndContextIsolation(t *testing.T) {
	var bufA, bufB bytes.Buffer
	logA := logging.New(logging.Config{Format: logging.FormatJSON}, logging.Options{
		Writer: &bufA,
		Attrs:  []slog.Attr{slog.String(logging.AttrService, "a")},
	})
	logB := logging.New(logging.Config{Format: logging.FormatJSON}, logging.Options{
		Writer: &bufB,
		Attrs:  []slog.Attr{slog.String(logging.AttrService, "b")},
	})

	ctxA := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "ra"))
	ctxB := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "rb"))
	logA.InfoContext(ctxA, "hello")
	logB.InfoContext(ctxB, "hello")

	var recA, recB map[string]any
	if err := json.Unmarshal(bufA.Bytes(), &recA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bufB.Bytes(), &recB); err != nil {
		t.Fatal(err)
	}
	if recA[logging.AttrService] != "a" || recA[logging.AttrRequestID] != "ra" {
		t.Fatalf("a=%v", recA)
	}
	if recB[logging.AttrService] != "b" || recB[logging.AttrRequestID] != "rb" {
		t.Fatalf("b=%v", recB)
	}
	if strings.Contains(bufA.String(), `"service":"b"`) || strings.Contains(bufB.String(), `"service":"a"`) {
		t.Fatal("loggers polluted each other")
	}
}

func TestDoesNotTouchDefault(t *testing.T) {
	before := slog.Default()
	_ = logging.New(logging.Config{Format: logging.FormatText}, logging.Options{Writer: &bytes.Buffer{}})
	if slog.Default() != before {
		t.Fatal("logging.New must not replace slog.Default")
	}
}

func TestConfigControlsLevelAndFormat(t *testing.T) {
	if err := logging.DefaultConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	if (logging.Config{Format: "xml"}).Validate() == nil {
		t.Fatal("accepted unknown format")
	}
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Level: slog.LevelWarn, Format: logging.FormatText}, logging.Options{Writer: &buf})
	logger.Info("hidden")
	logger.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), "level=WARN msg=shown") {
		t.Fatalf("output=%q", buf.String())
	}
}
