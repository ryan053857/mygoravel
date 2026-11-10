package logging_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	slogformatter "github.com/samber/slog-formatter"
	slogmulti "github.com/samber/slog-multi"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/logging/logtest"
)

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var rec map[string]any
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	return rec
}

func TestErrorFormatterExpandsErrorChain(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf})
	err := errorx.Define(errorx.Conflict, "article.slug_taken").Wrap(errors.New("pq: duplicate key"))
	logger.Error("failed", "error", err)

	got := decode(t, buf.Bytes())["error"].(map[string]any)
	if got["code"] != "article.slug_taken" || !strings.Contains(got["message"].(string), "duplicate key") {
		t.Fatal(got)
	}
	if !strings.Contains(got["stack"].(string), "pipeline_test.go") {
		t.Fatalf("stack must point at where the error was created: %v", got["stack"])
	}

	buf.Reset()
	logger.Error("plain", "error", plainErr("boom"))
	if got := decode(t, buf.Bytes())["error"].(map[string]any); len(got) != 1 || got["message"] != "boom" {
		t.Fatalf("plain errors only carry message: %v", got)
	}
}

type plainErr string

func (e plainErr) Error() string { return string(e) }

func TestFormatAppliesToEveryOutput(t *testing.T) {
	var base, extra bytes.Buffer
	logger := logging.New(logging.Config{Writer: &base},
		logging.Format(slogformatter.PIIFormatter("user")),
		logging.Fanout(slog.NewJSONHandler(&extra, nil)),
	)
	ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "r1"))
	logger.InfoContext(ctx, "signed up", slog.Group("user", "id", "u1", "email", "foobar@example.com"))

	for name, buf := range map[string]*bytes.Buffer{"base": &base, "extra": &extra} {
		rec := decode(t, buf.Bytes())
		user := rec["user"].(map[string]any)
		if user["id"] != "u1" || user["email"] != "foob*******" || rec["request_id"] != "r1" {
			t.Fatalf("%s: %v", name, rec)
		}
	}
}

func TestFanoutOutputsFilterByOwnLevel(t *testing.T) {
	var base, alerts bytes.Buffer
	logger := logging.New(logging.Config{Writer: &base, Service: "blog"},
		logging.Fanout(slog.NewJSONHandler(&alerts, &slog.HandlerOptions{Level: slog.LevelError})),
	)
	logger.Info("routine")
	logger.Error("broken")
	if strings.Count(base.String(), "\n") != 2 || strings.Count(alerts.String(), "\n") != 1 {
		t.Fatalf("base=%q alerts=%q", base.String(), alerts.String())
	}
	if decode(t, alerts.Bytes())["service"] != "blog" {
		t.Fatalf("fanout output misses service: %s", alerts.String())
	}
}

func TestPipeRunsBeforeOutputs(t *testing.T) {
	var buf bytes.Buffer
	dropHealth := slogmulti.NewHandleInlineMiddleware(func(ctx context.Context, r slog.Record, next func(context.Context, slog.Record) error) error {
		if r.Message == "health" {
			return nil
		}
		return next(ctx, r)
	})
	logger := logging.New(logging.Config{Writer: &buf}, logging.Pipe(dropHealth))
	logger.Info("health")
	logger.Info("kept")
	if strings.Contains(buf.String(), "health") || !strings.Contains(buf.String(), "kept") {
		t.Fatal(buf.String())
	}
}

func TestTimeZone(t *testing.T) {
	var buf bytes.Buffer
	logger := logging.New(logging.Config{Writer: &buf, TimeZone: "Asia/Shanghai"})
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	logger.Info("x", "at", at)
	rec := decode(t, buf.Bytes())
	if !strings.HasSuffix(rec["time"].(string), "+08:00") || rec["at"] != "2026-01-02T11:04:05+08:00" {
		t.Fatal(rec)
	}

	if (logging.Config{TimeZone: "Mars/Olympus"}).Validate() == nil {
		t.Fatal("accepted unknown time zone")
	}
}

func TestOrAddsErrorFormatterToForeignLoggers(t *testing.T) {
	var buf bytes.Buffer
	logging.Or(slog.New(slog.NewJSONHandler(&buf, nil))).Error("x", "error", errors.New("boom"))
	if got := decode(t, buf.Bytes())["error"].(map[string]any); got["message"] != "boom" || got["stack"] == nil {
		t.Fatal(got)
	}
	own := logging.New(logging.Config{Writer: &buf})
	if logging.Or(own) != own {
		t.Fatal("Or must not rewrap loggers built by New")
	}
}

func TestRecorder(t *testing.T) {
	rec := logtest.Capture(t)
	ctx := logging.WithAttrs(context.Background(), slog.String(logging.AttrRequestID, "r1"))
	slog.With("component", "c").WithGroup("response").ErrorContext(ctx, "http request", "status", 500, "size", 10)
	slog.Info("other", "error", errors.New("boom"))

	got, ok := rec.Find("http request")
	if !ok || got.Level != slog.LevelError {
		t.Fatal(rec.Records())
	}
	if got.Attr("response.status") != int64(500) || got.Attr("response.size") != int64(10) ||
		got.Attr("component") != "c" || got.Attr("response.request_id") != "r1" {
		t.Fatal(got.Attrs)
	}
	if other, _ := rec.Find("other"); other.Attr("error.message") != "boom" {
		t.Fatal(other.Attrs)
	}
	if !rec.Has(slog.LevelInfo, "other") || rec.Has(slog.LevelWarn, "other") {
		t.Fatal("Has mismatched")
	}
	rec.Reset()
	if len(rec.Records()) != 0 {
		t.Fatal("Reset kept records")
	}
}
