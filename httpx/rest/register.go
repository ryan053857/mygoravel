package rest

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"go.opentelemetry.io/otel/trace"

	"github.com/ryan053857/mygoravel/logging"
)

// operationID 是操作 ID 的格式：客户端生成器用它命名函数，例如 article-publish 生成 articlePublish；Go 客户端按第一段分组，生成 Article.Publish。
var operationID = regexp.MustCompile(`^[a-z][a-z0-9]*(-[a-z0-9]+)*$`)

// Register 登记端点。op.OperationID 必须是 kebab-case，写成“资源-动作”，例如 article-publish。handler 通常是 action 的方法值，例如 article.NewPublish(db).HTTP；
// ModeOpenAPI 只生成文档，不调用 handler，可以传 nil。
// 处理器错误经 FromError 转换为 HTTP 错误，业务错误的 detail 按请求语言翻译。
func Register[I, O any](api *API, op huma.Operation, handler func(context.Context, *I) (*O, error)) {
	register(api, op, nil, handler)
}

// register 是 Register 和 RegisterSSE 的共同实现，events 只有 SSE 端点才有。
func register[I, O any](api *API, op huma.Operation, events map[string]reflect.Type, handler func(context.Context, *I) (*O, error)) {
	if api == nil {
		panic("rest: nil API")
	}
	if !operationID.MatchString(op.OperationID) {
		panic("rest: operation ID must be kebab-case, e.g. article-publish; got " + strconv.Quote(op.OperationID))
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
				logging.RecordError(trace.SpanFromContext(ctx), err)
			}
			return out, se
		}
	}
	api.names.bodies(reflect.TypeFor[I](), reflect.TypeFor[O]())
	huma.Register(api.API, op, handler)
	api.endpoints = append(api.endpoints, Endpoint{
		OperationID: op.OperationID,
		Method:      op.Method,
		Path:        op.Path,
		Summary:     op.Summary,
		Description: op.Description,
		Public:      len(op.Security) == 0,
		Input:       reflect.TypeFor[I](),
		Output:      reflect.TypeFor[O](),
		Events:      events,
	})
}

// Endpoint 描述一个用 Register 或 RegisterSSE 登记的端点，供客户端代码生成器使用。
type Endpoint struct {
	OperationID string
	Method      string
	// Path 是登记时的路径模板，不含 Options.BasePath，例如 /articles/{id}。
	Path        string
	Summary     string
	Description string
	// Public 表示端点用 Public 声明，不需要凭据。
	Public bool
	// Input 和 Output 是处理器的输入、输出类型（不是指针）。
	Input  reflect.Type
	Output reflect.Type
	// Events 是 SSE 端点的事件名到事件数据类型（不是指针），其他端点为 nil。
	Events map[string]reflect.Type
}

// Endpoints 按登记顺序返回已登记的端点。
func (a *API) Endpoints() []Endpoint { return slices.Clone(a.endpoints) }

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
