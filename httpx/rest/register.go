package rest

import (
	"context"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
)

// Register 登记端点。ModeServe 调用 newHandler，ModeOpenAPI 安装文档占位处理器。
// 处理器错误经 FromError 转换为 HTTP 错误，业务错误的 detail 按请求语言翻译。
func Register[I, O any](api *API, op huma.Operation, newHandler func() func(context.Context, *I) (*O, error)) {
	if api == nil {
		panic("rest: nil API")
	}
	if op.Security == nil {
		panic("rest: endpoint must explicitly declare Public, RequireBearer or another RequireAuth scheme")
	}
	if len(op.Security) > 0 && authScheme(&op) == "" {
		panic("rest: security must be declared through RequireAuth so it binds runtime validation")
	}
	// 从 OpenAPI 路径表检查操作 ID 和路由冲突。
	for path, item := range api.OpenAPI().Paths {
		for _, existing := range []*huma.Operation{item.Get, item.Post, item.Put, item.Patch, item.Delete, item.Head, item.Options, item.Trace} {
			if existing == nil {
				continue
			}
			if existing.OperationID == strings.ReplaceAll(op.OperationID, " ", "-") || (existing.Method == op.Method && routeShape(path) == routeShape(op.Path)) {
				panic("rest: duplicate operation ID or conflicting route")
			}
		}
	}
	var handler func(context.Context, *I) (*O, error)
	if api.mode == ModeOpenAPI {
		handler = func(context.Context, *I) (*O, error) {
			return nil, errors.New("rest: schema-only handler invoked")
		}
	} else {
		if newHandler == nil {
			panic("rest: nil newHandler")
		}
		h := newHandler()
		if h == nil {
			panic("rest: newHandler returned nil")
		}
		handler = func(ctx context.Context, in *I) (*O, error) {
			out, err := h(ctx, in)
			if err == nil {
				return out, nil
			}
			se := api.httpError(ctx, err)
			if se.GetStatus() >= 500 {
				api.logger.ErrorContext(ctx, "request failed", "error", err)
			}
			return out, se
		}
	}
	huma.Register(api.API, op, handler)
}

func routeShape(path string) string {
	var b strings.Builder
	inside := false
	for _, c := range path {
		if c == '{' {
			inside = true
			b.WriteString("{}")
		} else if c == '}' {
			inside = false
		} else if !inside {
			b.WriteRune(c)
		}
	}
	return b.String()
}
