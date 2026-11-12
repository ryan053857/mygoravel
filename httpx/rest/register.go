package rest

import (
	"context"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
)

// operationID 是操作 ID 的格式：客户端生成器用它命名函数，例如 publish-article 生成 publishArticle。
var operationID = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Register 登记端点。op.OperationID 必须是 kebab-case，例如 publish-article。handler 通常是 action 的方法值，例如 article.NewPublish(db).HTTP；
// ModeOpenAPI 只生成文档，不调用 handler，可以传 nil。
// 处理器错误经 FromError 转换为 HTTP 错误，业务错误的 detail 按请求语言翻译。
func Register[I, O any](api *API, op huma.Operation, handler func(context.Context, *I) (*O, error)) {
	if api == nil {
		panic("rest: nil API")
	}
	if !operationID.MatchString(op.OperationID) {
		panic("rest: operation ID must be kebab-case, e.g. publish-article; got " + strconv.Quote(op.OperationID))
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
			if existing.OperationID == op.OperationID || (existing.Method == op.Method && routeShape(path) == routeShape(op.Path)) {
				panic("rest: duplicate operation ID or conflicting route")
			}
		}
	}
	h := handler
	if api.mode == ModeOpenAPI {
		handler = func(context.Context, *I) (*O, error) {
			return nil, errors.New("rest: schema-only handler invoked")
		}
	} else {
		if h == nil {
			panic("rest: nil handler")
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
	api.names.bodies(reflect.TypeFor[I](), reflect.TypeFor[O]())
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
