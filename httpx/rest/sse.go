package rest

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
)

// RegisterSSE 登记一个以 text/event-stream 响应的端点。events 的键是事件名，值是该事件数据的类型，
// 用于生成 OpenAPI 文档。
//
// 处理器先完成检查和准备，出错时照常返回错误，客户端收到 Problem Details；
// 准备好后返回 SSE(fn)，fn 在连接上写出事件，不受 httpx.Timeout 的超时限制。
func RegisterSSE[I any](api *API, op huma.Operation, events map[string]any, newHandler func() func(context.Context, *I) (*huma.StreamResponse, error)) {
	addEventStream(api, &op, events)
	Register(api, op, newHandler)
}

// SSE 返回一个 SSE 响应，fn 用 Stream.SendJSON 写出事件，连接管理见 httpx.SSE。
// 开始写事件之后，fn 返回的错误只记录日志。
func SSE(fn func(s *httpx.Stream) error) *huma.StreamResponse {
	return &huma.StreamResponse{Body: func(hctx huma.Context) {
		r, w := humachi.Unwrap(hctx)
		err := httpx.SSE(w, r, fn)
		if err != nil && !errors.Is(err, context.Canceled) {
			logging.Or(nil).ErrorContext(r.Context(), "event stream failed", logging.AttrComponent, "http", "error", err)
		}
	}}
}

// addEventStream 按 Huma sse 包的格式在 200 响应中描述 text/event-stream。
func addEventStream(api *API, op *huma.Operation, events map[string]any) {
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	if op.Responses["200"] == nil {
		op.Responses["200"] = &huma.Response{Description: "Server-Sent Events"}
	}
	if op.Responses["200"].Content == nil {
		op.Responses["200"].Content = map[string]*huma.MediaType{}
	}
	registry := api.OpenAPI().Components.Schemas
	var schemas []*huma.Schema
	for name, v := range events {
		t := reflect.TypeOf(v)
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		schemas = append(schemas, &huma.Schema{
			Title: "Event " + name,
			Type:  huma.TypeObject,
			Properties: map[string]*huma.Schema{
				"id":    {Type: huma.TypeString, Description: "The event ID."},
				"event": {Type: huma.TypeString, Description: "The event name.", Extensions: map[string]any{"const": name}},
				"data":  registry.Schema(t, true, name),
			},
			Required: []string{"event", "data"},
		})
	}
	slices.SortFunc(schemas, func(a, b *huma.Schema) int { return strings.Compare(a.Title, b.Title) })
	op.Responses["200"].Content["text/event-stream"] = &huma.MediaType{Schema: &huma.Schema{
		Title:       "Server Sent Events",
		Description: "Each oneOf object represents one possible event, serialized as UTF-8 text according to the SSE specification.",
		Type:        huma.TypeArray,
		Items:       &huma.Schema{Extensions: map[string]any{"oneOf": schemas}},
	}}
}
