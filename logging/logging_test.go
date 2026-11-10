package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/logging"
)

func TestNewAndContextIsolation(t *testing.T) {
	var bufA, bufB bytes.Buffer
	logA := logging.New(logging.Config{Service: "a", Writer: &bufA})
	logB := logging.New(logging.Config{Service: "b", Writer: &bufB})

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
	_ = logging.New(logging.Config{Format: logging.FormatText, Writer: &bytes.Buffer{}})
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
	logger := logging.New(logging.Config{Level: slog.LevelWarn, Format: logging.FormatText, Writer: &buf})
	logger.Info("hidden")
	logger.Warn("shown")
	if strings.Contains(buf.String(), "hidden") || !strings.Contains(buf.String(), " WRN shown") {
		t.Fatalf("output=%q", buf.String())
	}
}

func TestTextFormatIsPlainWhenNotATerminal(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Format: logging.FormatText, TimeZone: "UTC", Writer: &buf})
	ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "r1"))
	logger.ErrorContext(ctx, "failed", "error", plainErr("boom"), slog.Group("response", "status", 500))
	out := buf.String()
	if strings.Contains(out, "\x1b[") {
		t.Fatalf("colored output to a buffer: %q", out)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}\.\d{3} ERR failed `).MatchString(out) ||
		!strings.Contains(out, "error.message=boom") || !strings.Contains(out, "response.status=500") || !strings.Contains(out, "request_id=r1") {
		t.Fatalf("output=%q", out)
	}
}

func TestVersionAttr(t *testing.T) {
	var buf bytes.Buffer
	logging.New(logging.Config{Version: "v1.2.3", Writer: &buf}).Info("x")
	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatal(err)
	}
	if rec[logging.AttrVersion] != "v1.2.3" {
		t.Fatalf("rec=%v", rec)
	}
	if _, ok := rec[logging.AttrService]; ok {
		t.Fatalf("empty service should be omitted: %v", rec)
	}
}

func TestOrFallsBackToDefault(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "r1"))
	logging.Or(nil).InfoContext(ctx, "hello")
	if !strings.Contains(buf.String(), `"request_id":"r1"`) {
		t.Fatalf("output=%q", buf.String())
	}

	var own bytes.Buffer
	logging.Or(slog.New(slog.NewJSONHandler(&own, nil))).Info("mine")
	if !strings.Contains(own.String(), "mine") || strings.Contains(buf.String(), "mine") {
		t.Fatal("Or ignored the given logger")
	}
}
