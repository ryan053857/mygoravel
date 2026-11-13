package mcpx

import (
	"context"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/go-tenon/tenon/logging"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// scopeName 是 MCP 指标和链路的 instrumentation scope。
const scopeName = "github.com/go-tenon/tenon/mcpx"

// durationBuckets 是 OpenTelemetry MCP 语义约定规定的耗时分桶（秒）。
var durationBuckets = []float64{0.01, 0.02, 0.05, 0.1, 0.2, 0.5, 1, 2, 5, 10, 30, 60, 120, 300}

// 属性名遵循 OpenTelemetry 的 MCP 语义约定。
const (
	attrMethod          = attribute.Key("mcp.method.name")
	attrProtocolVersion = attribute.Key("mcp.protocol.version")
	attrTool            = attribute.Key("gen_ai.tool.name")
	attrOperation       = attribute.Key("gen_ai.operation.name")
	attrErrorType       = attribute.Key("error.type")
	attrTransport       = attribute.Key("network.transport")
)

// WithTelemetry 让 Server 报告 OpenTelemetry 指标和链路，属性遵循 OpenTelemetry 的 MCP 语义约定。
//
// 指标 mcp.server.operation.duration（直方图，秒）记录每个请求的处理耗时，属性有 mcp.method.name
// （tools/call、tools/list、initialize 等）、gen_ai.tool.name（登记过的工具才有）、mcp.protocol.version、
// network.transport（HTTP 为 tcp，stdio 为 pipe），出错时还有 error.type：工具报告错误时是 tool_error，
// JSON-RPC 错误是错误码。调用不存在或没有权限的工具（-32602）属于调用方的问题，不算出错。
//
// 每个请求是一个 server span，名称是方法和工具名，例如 tools/call article_publish。请求的 params._meta 中
// 带有 W3C traceparent 时，span 接在客户端的链路之后，并链接到 HTTP 请求的 span；没有时接在 HTTP 请求的 span 之下。
// 工具中执行的数据库语句、发布的任务都在这条链路中。
//
// mp 或 tp 为 nil 时 panic；只需要其中一种时，另一个传 noop 实现。
func WithTelemetry(mp metric.MeterProvider, tp trace.TracerProvider) ServerOption {
	if mp == nil || tp == nil {
		panic("mcpx: nil meter or tracer provider")
	}
	duration, err := mp.Meter(scopeName).Float64Histogram("mcp.server.operation.duration", metric.WithUnit("s"),
		metric.WithDescription("MCP request or notification duration as observed on the receiver."),
		metric.WithExplicitBucketBoundaries(durationBuckets...))
	if err != nil {
		panic(errors.Wrap(err, "mcpx: create mcp.server.operation.duration"))
	}
	tracer := tp.Tracer(scopeName)
	return func(s *Server) {
		// 后添加的中间件在外层：被拒绝的调用同样被记录。
		s.sdk.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
				return s.observe(ctx, method, req, next, tracer, duration)
			}
		})
	}
}

func (s *Server) observe(ctx context.Context, method string, req mcp.Request, next mcp.MethodHandler,
	tracer trace.Tracer, duration metric.Float64Histogram) (mcp.Result, error) {
	start := time.Now()
	attrs := []attribute.KeyValue{attrMethod.String(method), attrTransport.String("pipe")}
	if extra := req.GetExtra(); extra != nil && extra.Header != nil {
		attrs[1] = attrTransport.String("tcp")
	}
	name := method
	if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && method == "tools/call" {
		attrs = append(attrs, attrOperation.String("execute_tool"))
		// 只有登记过的工具名才作为属性和 span 名，模型编造的名称不会制造新的指标序列。
		if _, known := s.tools[p.Name]; known {
			attrs = append(attrs, attrTool.String(p.Name))
			name += " " + p.Name
		}
	}
	if v, ok := req.(interface{ ProtocolVersion() string }); ok && v.ProtocolVersion() != "" {
		attrs = append(attrs, attrProtocolVersion.String(v.ProtocolVersion()))
	}

	opts := []trace.SpanStartOption{trace.WithSpanKind(trace.SpanKindServer)}
	spanCtx := ctx
	if parent := remoteParent(ctx, req); parent.IsValid() {
		// 客户端在 _meta 中带来的链路是父级，HTTP 请求的 span 作为链接。
		if ambient := trace.SpanContextFromContext(ctx); ambient.IsValid() {
			opts = append(opts, trace.WithLinks(trace.Link{SpanContext: ambient}))
		}
		spanCtx = trace.ContextWithRemoteSpanContext(ctx, parent)
	}
	spanCtx, span := tracer.Start(spanCtx, name, opts...)
	defer span.End()

	res, err := next(spanCtx, method, req)

	var errType string
	switch r := res.(type) {
	case *mcp.CallToolResult:
		if err == nil && r != nil && r.IsError {
			errType = "tool_error"
		}
	}
	if err != nil {
		errType = jsonrpcErrorType(err)
		if errType != "" {
			logging.RecordError(span, err)
		}
	}
	if errType != "" {
		attrs = append(attrs, attrErrorType.String(errType))
		span.SetStatus(codes.Error, errType)
	}
	span.SetAttributes(attrs...)
	duration.Record(spanCtx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	return res, err
}

// remoteParent 返回请求的 params._meta 中 traceparent 给出的链路。
func remoteParent(ctx context.Context, req mcp.Request) trace.SpanContext {
	params, ok := req.GetParams().(interface{ GetMeta() map[string]any })
	if !ok || params == nil {
		return trace.SpanContext{}
	}
	meta := params.GetMeta()
	carrier := propagation.MapCarrier{}
	for _, key := range []string{"traceparent", "tracestate"} {
		if v, ok := meta[key].(string); ok {
			carrier[key] = v
		}
	}
	if len(carrier) == 0 {
		return trace.SpanContext{}
	}
	return trace.SpanContextFromContext(propagation.TraceContext{}.Extract(context.Background(), carrier))
}

// jsonrpcErrorType 返回 JSON-RPC 错误的 error.type：错误码的字符串。调用方的问题（解析失败、无效请求、
// 方法不存在、参数无效、资源不存在）不算出错，返回空字符串；不是 JSON-RPC 错误时返回 _OTHER。
func jsonrpcErrorType(err error) string {
	var rpcErr *jsonrpc.Error
	if !errors.As(err, &rpcErr) {
		return "_OTHER"
	}
	switch rpcErr.Code {
	case jsonrpc.CodeParseError, jsonrpc.CodeInvalidRequest, jsonrpc.CodeMethodNotFound, jsonrpc.CodeInvalidParams, -32002:
		return ""
	}
	return strconv.FormatInt(rpcErr.Code, 10)
}
