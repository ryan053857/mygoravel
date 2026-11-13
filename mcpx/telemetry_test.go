package mcpx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/go-tenon/tenon/identity"
	"github.com/go-tenon/tenon/mcpx"
)

// rpc 以 editor 的身份向 h 发送一个 JSON-RPC 请求，ctx 是 HTTP 请求的 context。
func rpc(t *testing.T, ctx context.Context, h http.Handler, body string) string {
	t.Helper()
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer editor")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	return w.Body.String()
}

func attrs(kvs []attribute.KeyValue) map[attribute.Key]string {
	m := map[attribute.Key]string{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value.String()
	}
	return m
}

func TestTelemetry(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))
	srv := mcpx.New(mcpx.Options{Name: "blog"}, mcpx.WithTelemetry(sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)), tp))
	registerTools(srv)
	h := mcpx.Handler(srv, mcpx.HandlerOptions{
		Validate: func(context.Context, string) (identity.Subject, error) { return editor, nil },
		Accounts: identity.Accounts{"editor": nil},
		Tenant:   func(*http.Request) string { return "acme" },
	})

	// HTTP 请求的 span：没有客户端链路时 MCP 的 span 接在它之下。
	httpCtx, httpSpan := tp.Tracer("http").Start(context.Background(), "POST /mcp")
	body := rpc(t, httpCtx, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"article_publish","arguments":{"id":"0193a5c2-7d1e-7b3a-9f61-2c4e8d0a1b2c"}}}`)
	if !strings.Contains(body, `"by":"e1"`) {
		t.Fatalf("result %s", body)
	}
	rpc(t, httpCtx, h, `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"article_publish","arguments":{"id":"0193a5c2-7d1e-7b3a-9f61-2c4e8d0a1b2c","fail":"conflict"}}}`)
	rpc(t, httpCtx, h, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"made_up_tool","arguments":{}}}`)
	httpSpan.End()

	// 客户端在 _meta 中带来链路：以它为父级，链接 HTTP 请求的 span。
	const traceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	rpc(t, httpCtx, h, `{"jsonrpc":"2.0","id":4,"method":"tools/list","params":{"_meta":{"traceparent":"`+traceparent+`"}}}`)

	var mcpSpans []sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if s.InstrumentationScope().Name == "github.com/go-tenon/tenon/mcpx" {
			mcpSpans = append(mcpSpans, s)
		}
	}
	if len(mcpSpans) != 4 {
		t.Fatalf("%d mcp spans", len(mcpSpans))
	}
	ok, failed, unknown, list := mcpSpans[0], mcpSpans[1], mcpSpans[2], mcpSpans[3]
	a := attrs(ok.Attributes())
	if ok.Name() != "tools/call article_publish" || ok.SpanKind() != trace.SpanKindServer ||
		ok.Parent().SpanID() != httpSpan.SpanContext().SpanID() ||
		a["mcp.method.name"] != "tools/call" || a["gen_ai.tool.name"] != "article_publish" ||
		a["gen_ai.operation.name"] != "execute_tool" || a["network.transport"] != "tcp" ||
		a["mcp.protocol.version"] != "2025-06-18" || a["error.type"] != "" {
		t.Fatalf("ok span %s %v", ok.Name(), a)
	}
	if fa := attrs(failed.Attributes()); fa["error.type"] != "tool_error" || failed.Status().Description != "tool_error" {
		t.Fatalf("failed span %v %v", fa, failed.Status())
	}
	// 模型编造的工具名不进入 span 名和属性；调用方的问题不算出错。
	if ua := attrs(unknown.Attributes()); unknown.Name() != "tools/call" || ua["gen_ai.tool.name"] != "" || ua["error.type"] != "" {
		t.Fatalf("unknown span %s %v", unknown.Name(), ua)
	}
	if list.Name() != "tools/list" || list.Parent().TraceID().String() != "4bf92f3577b34da6a3ce929d0e0e4736" ||
		len(list.Links()) != 1 || list.Links()[0].SpanContext.SpanID() != httpSpan.SpanContext().SpanID() {
		t.Fatalf("list span parent %v links %v", list.Parent(), list.Links())
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	m := rm.ScopeMetrics[0].Metrics[0]
	if m.Name != "mcp.server.operation.duration" || m.Unit != "s" {
		t.Fatalf("metric %s %s", m.Name, m.Unit)
	}
	var toolErrors, calls uint64
	for _, dp := range m.Data.(metricdata.Histogram[float64]).DataPoints {
		if v, ok := dp.Attributes.Value("gen_ai.tool.name"); ok && v.AsString() == "article_publish" {
			calls += dp.Count
		}
		if v, ok := dp.Attributes.Value("error.type"); ok && v.AsString() == "tool_error" {
			toolErrors += dp.Count
		}
	}
	if calls != 2 || toolErrors != 1 {
		t.Fatalf("calls %d, tool errors %d", calls, toolErrors)
	}
}

func TestWithTelemetryNilProvider(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil provider accepted")
		}
	}()
	mcpx.WithTelemetry(metricnoop.NewMeterProvider(), nil)
}
