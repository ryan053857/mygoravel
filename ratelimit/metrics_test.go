package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/go-tenon/tenon/ratelimit"
)

// failing 返回预先设定的错误。
type failing struct{ err error }

func (f failing) Allow(context.Context, string) error { return f.err }
func (f failing) Reset(context.Context, string) error { return nil }

// counts 返回 ratelimit.requests 按 name/result 的累计值。
func counts(t *testing.T, reader *sdkmetric.ManualReader) map[string]int64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "ratelimit.requests" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				name, _ := dp.Attributes.Value(attribute.Key("ratelimit.name"))
				result, _ := dp.Attributes.Value(attribute.Key("ratelimit.result"))
				out[name.AsString()+"/"+result.AsString()] = dp.Value
			}
		}
	}
	return out
}

func TestObserve(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	ctx := context.Background()

	login := ratelimit.Observe(mp, "login", ratelimit.NewMemory(ratelimit.Config{Rate: 2, Interval: time.Hour, Burst: 2}))
	for range 3 {
		_ = login.Allow(ctx, "a@example.com")
	}
	if err := login.Allow(ctx, "b@example.com"); err != nil {
		t.Fatalf("other key limited: %v", err)
	}
	// 包装后错误原样返回。
	if err := login.Allow(ctx, "a@example.com"); !errors.Is(err, ratelimit.ErrExceeded) {
		t.Fatalf("err = %v", err)
	}

	down := errors.New("redis down")
	broken := ratelimit.Observe(mp, "api", failing{down})
	if err := broken.Allow(ctx, "k"); !errors.Is(err, down) {
		t.Fatalf("err = %v", err)
	}
	// 调用方取消或超时不算限流器出错。
	for _, err := range []error{context.Canceled, errors.Wrap(context.DeadlineExceeded, "query")} {
		if got := ratelimit.Observe(mp, "api", failing{err}).Allow(ctx, "k"); !errors.Is(got, err) {
			t.Fatalf("err = %v", got)
		}
	}

	got := counts(t, reader)
	want := map[string]int64{"login/allowed": 3, "login/exceeded": 2, "api/error": 1}
	if len(got) != len(want) {
		t.Fatalf("counts %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("counts %v, want %v", got, want)
		}
	}
}

func TestObservePanics(t *testing.T) {
	mp := sdkmetric.NewMeterProvider()
	l := ratelimit.NewMemory(ratelimit.Config{Rate: 1, Interval: time.Second, Burst: 1})
	for name, f := range map[string]func(){
		"nil provider": func() { ratelimit.Observe(nil, "x", l) },
		"empty name":   func() { ratelimit.Observe(mp, "", l) },
		"nil limiter":  func() { ratelimit.Observe(mp, "x", nil) },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			f()
		}()
	}
}
