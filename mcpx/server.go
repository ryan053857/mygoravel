package mcpx

import (
	"context"
	"encoding/json"
	"log/slog"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
)

// toolName 是工具名的格式：小写字母开头，小写字母、数字和下划线，例如 article_publish。
var toolName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

// Options 是 New 的选项。
type Options struct {
	// Name 和 Version 在初始化时告诉客户端这是哪个服务，Name 必填；Version 为空时是 0.0.0。
	Name    string
	Version string
	// Instructions 告诉模型这个服务是做什么的、工具之间怎么配合，例如“文章 ID 是 UUID，先用 article_search 查找”。
	Instructions string
	// Logger 记录工具中的内部错误，nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Server 是一个 MCP 服务：登记的工具，以及调用它们时的权限检查、参数校验和错误转换。
// 用 Handler 挂到 HTTP 路由上；同一个 Server 可以同时用于多个入口。
type Server struct {
	sdk    *mcp.Server
	logger *slog.Logger
	tools  map[string]*registered
}

// registered 是登记的一个工具。
type registered struct {
	spec Tool
	sdk  *mcp.Tool
}

// New 创建没有工具的 Server。Name 为空时 panic。
func New(opts Options) *Server {
	if opts.Name == "" {
		panic("mcpx: server name is required")
	}
	if opts.Version == "" {
		opts.Version = "0.0.0"
	}
	s := &Server{
		logger: logging.Or(opts.Logger).With(logging.AttrComponent, "mcp"),
		tools:  map[string]*registered{},
	}
	s.sdk = mcp.NewServer(&mcp.Implementation{Name: opts.Name, Version: opts.Version},
		&mcp.ServerOptions{Instructions: opts.Instructions, Logger: s.sdkLogger()})
	s.sdk.AddReceivingMiddleware(s.authorize)
	return s
}

// sdkLogger 返回交给 SDK 的日志器，只保留 Warn 及以上：服务不保存会话，SDK 为每个请求记录的连接、断开都是噪音。
func (s *Server) sdkLogger() *slog.Logger {
	return slog.New(warnOnly{s.logger.Handler()})
}

type warnOnly struct{ slog.Handler }

func (h warnOnly) Enabled(ctx context.Context, l slog.Level) bool {
	return l >= slog.LevelWarn && h.Handler.Enabled(ctx, l)
}
func (h warnOnly) WithAttrs(attrs []slog.Attr) slog.Handler {
	return warnOnly{h.Handler.WithAttrs(attrs)}
}
func (h warnOnly) WithGroup(name string) slog.Handler { return warnOnly{h.Handler.WithGroup(name)} }

// SDK 返回底层的 *mcp.Server，用于直接添加 resource、prompt 等本包没有包装的功能。
// 用它添加的工具不经过本包的权限检查和参数校验。
func (s *Server) SDK() *mcp.Server { return s.sdk }

// Tool 描述一个工具。模型只看得到 Name、Title、Description、参数的说明和注解，
// 它们决定了模型会不会、会怎样调用这个工具，像写给新同事的说明一样写清楚。
type Tool struct {
	// Name 是工具名，小写字母开头，只有小写字母、数字和下划线，最长 64 个字符，例如 article_publish。
	Name string
	// Title 是给人看的名称，例如“发布文章”，可以为空。
	Title string
	// Description 说明工具做什么、什么时候用、有什么限制，必填。
	Description string
	// Permissions 是调用这个工具需要的全部权限，用 identity.Subject.Can 检查：
	// 主体缺少其中任何一项时，工具不出现在列表中，调用时与不存在的工具相同。
	Permissions []string
	// ReadOnly 表示工具不修改任何数据。客户端通常不需要用户确认就调用只读工具。
	ReadOnly bool
	// Destructive 表示工具会删除或覆盖数据，客户端据此在调用前请用户确认。ReadOnly 时忽略。
	Destructive bool
	// Idempotent 表示用相同的参数重复调用没有额外的效果。ReadOnly 时忽略。
	Idempotent bool
}

// Register 把 fn 登记为工具 t。fn 通常是 action 的方法值，例如 article.NewPublish(db).Tool。
//
// I 必须是结构体，它的 JSON Schema 和校验规则按 Huma 的标签生成（doc、minLength、enum、format 等），
// 与 REST 的请求体相同，同一个类型可以同时用作两者。参数不符合 schema 时 fn 不会被调用，
// 模型收到各字段的错误。O 是结构体或 map 时生成 outputSchema，结果同时以结构化内容和 JSON 文本返回；
// 其他类型（例如切片）只以 JSON 文本返回。
//
// fn 返回的业务错误（errorx 的 4xx 类错误）作为工具的错误结果返回给模型，内容是错误码和按请求语言翻译的消息，
// 模型可以据此调整；其他错误和 panic 只告诉模型“内部错误”和请求 ID，完整错误记录在日志中。
//
// 名称不合法、重复、Description 为空、I 不是结构体或 fn 为 nil 时 panic。
func Register[I, O any](s *Server, t Tool, fn func(context.Context, I) (O, error)) {
	switch {
	case s == nil:
		panic("mcpx: nil server")
	case fn == nil:
		panic("mcpx: nil handler for tool " + strconv.Quote(t.Name))
	case !toolName.MatchString(t.Name):
		panic("mcpx: tool name must be lowercase letters, digits and underscores, e.g. article_publish; got " + strconv.Quote(t.Name))
	case strings.TrimSpace(t.Description) == "":
		panic("mcpx: tool " + strconv.Quote(t.Name) + " needs a description")
	}
	if _, ok := s.tools[t.Name]; ok {
		panic("mcpx: duplicate tool " + strconv.Quote(t.Name))
	}
	in := newSchema(reflect.TypeFor[I](), huma.ModeWriteToServer)
	if in.typ != "object" {
		panic("mcpx: tool " + strconv.Quote(t.Name) + " input must be a struct")
	}
	out := newSchema(reflect.TypeFor[O](), huma.ModeReadFromServer)
	t.Permissions = slices.Clone(t.Permissions)
	tool := &mcp.Tool{
		Name:        t.Name,
		Title:       t.Title,
		Description: t.Description,
		InputSchema: in.json,
		Annotations: annotations(t),
	}
	structured := out.typ == "object"
	if structured {
		tool.OutputSchema = out.json
	}
	name := t.Name
	s.sdk.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (_ *mcp.CallToolResult, err error) {
		httpx.AddAccessLogAttrs(ctx, slog.String("mcp.tool", name))
		var input I
		if problems := in.validate(req.Params.Arguments, &input); len(problems) > 0 {
			return invalid(problems), nil
		}
		output, err := call(ctx, fn, input)
		if err != nil {
			return s.failure(ctx, name, err), nil
		}
		text, err := json.Marshal(output)
		if err != nil {
			return s.failure(ctx, name, err), nil
		}
		res := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}}
		if structured && string(text) != "null" {
			res.StructuredContent = json.RawMessage(text)
		}
		return res, nil
	})
	s.tools[t.Name] = &registered{spec: t, sdk: tool}
}

// Tools 返回登记的全部工具，按名称排序，用于检查暴露了哪些工具和它们需要的权限。
func (s *Server) Tools() []Tool {
	tools := make([]Tool, 0, len(s.tools))
	for _, r := range s.tools {
		t := r.spec
		t.Permissions = slices.Clone(t.Permissions)
		tools = append(tools, t)
	}
	slices.SortFunc(tools, func(a, b Tool) int { return strings.Compare(a.Name, b.Name) })
	return tools
}

func annotations(t Tool) *mcp.ToolAnnotations {
	closed := false
	a := &mcp.ToolAnnotations{Title: t.Title, ReadOnlyHint: t.ReadOnly, OpenWorldHint: &closed}
	if !t.ReadOnly {
		destructive := t.Destructive
		a.DestructiveHint = &destructive
		a.IdempotentHint = t.Idempotent
	}
	return a
}

// call 调用 fn，把 panic 转换为错误。
func call[I, O any](ctx context.Context, fn func(context.Context, I) (O, error), in I) (out O, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = errors.Newf("mcpx: panic: %v", r)
		}
	}()
	return fn(ctx, in)
}

// failure 把 fn 返回的错误转换为工具的错误结果：业务错误给出错误码和翻译后的消息，其他错误只给出请求 ID。
func (s *Server) failure(ctx context.Context, tool string, err error) *mcp.CallToolResult {
	var text string
	if code := errorx.CodeOf(err); code != "" && httpx.StatusOf(err) < 500 {
		text = code + ": " + httpx.ErrorDetail(ctx, err)
	} else {
		s.logger.ErrorContext(ctx, "tool failed", "mcp.tool", tool, "error", err)
		text = "internal error"
		if httpx.StatusOf(err) == 503 {
			text = "service temporarily unavailable, try again later"
		}
		if id := httpx.RequestID(ctx); id != "" {
			text += " (request " + id + ")"
		}
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// invalid 返回参数校验失败的结果，每行一个字段的错误。
func invalid(problems []string) *mcp.CallToolResult {
	text := "invalid arguments:\n- " + strings.Join(problems, "\n- ")
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}}
}

// authorize 是接收请求的中间件：工具列表只返回主体有权限的工具；调用没有权限的工具与调用不存在的工具相同。
// 主体来自 ctx（Handler 或 stdio 入口写入）；没有主体时只能使用不需要权限的工具。
func (s *Server) authorize(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		sub, _ := identity.FromContext(ctx)
		switch method {
		case "tools/call":
			if p, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
				if r, ok := s.tools[p.Name]; ok && !allowed(sub, r.spec.Permissions) {
					return nil, &jsonrpc.Error{Code: jsonrpc.CodeInvalidParams, Message: "unknown tool " + strconv.Quote(p.Name)}
				}
			}
		case "tools/list":
			res, err := next(ctx, method, req)
			if list, ok := res.(*mcp.ListToolsResult); ok && err == nil {
				list.Tools = slices.DeleteFunc(slices.Clone(list.Tools), func(t *mcp.Tool) bool {
					r, ok := s.tools[t.Name]
					return ok && !allowed(sub, r.spec.Permissions)
				})
			}
			return res, err
		}
		return next(ctx, method, req)
	}
}

func allowed(sub identity.Subject, permissions []string) bool {
	for _, p := range permissions {
		if !sub.Can(p) {
			return false
		}
	}
	return true
}
