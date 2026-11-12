package mcpx

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

// HandlerOptions 是 Handler 的选项。
type HandlerOptions struct {
	// Validate 校验请求头 Authorization: Bearer 中的 token，必填。与 REST 用同一个校验器，
	// 例如 apitoken.Store 的 Validate，或者 rest.TokenValidator 类型的值。
	Validate func(ctx context.Context, token string) (identity.Subject, error)
	// Accounts 是接受的账号体系和各自的 Resolver，与 rest.Options.Accounts 相同，必填。
	Accounts identity.Accounts
	// Tenant 从请求中取出本次请求所在的租户，例如子域名或请求头；nil 表示平台本身（""）。
	// 凭据绑定了租户时，两者必须相同。
	Tenant func(r *http.Request) string
	// Resource 不为 nil 时，这个端点是 OAuth 受保护资源：未认证的 401 响应带上
	// WWW-Authenticate: Bearer resource_metadata="…"，客户端据此找到授权服务器。
	// 同时把 ResourceMetadata 挂在 Resource.MetadataPath() 上，并用 apitoken.Store 的 ValidateFor(Resource.URL) 作为 Validate，
	// 只接受发给这个地址的 OAuth 令牌。
	Resource *Resource
	// Permissions 是使用这个端点需要的权限，例如 mcp:use；为空时不额外要求。
	// 个人访问令牌的 Scopes 中也要有它，令牌才能用于 MCP。
	Permissions []string
}

// Handler 返回以 Streamable HTTP 提供 s 的处理器，挂在一个路由上，例如 router.Handle("/mcp", h)。
//
// 每个请求先按 identity.Establish 确立主体，规则与 rest.RequireBearer 相同：没有或无效的 token 返回 401
// 并带 WWW-Authenticate: Bearer，凭据绑定了其他租户或缺少 Permissions 返回 403，认证后端故障返回 5xx。
// 主体写入 ctx，工具中用 identity.FromContext 取出。
//
// 服务不保存会话：每个请求独立处理，响应是 JSON，多个实例之间不需要粘性会话或共享存储。
// 因此服务端不能主动向客户端发请求（sampling、elicitation）或发送进度通知。
// 请求体大小、超时、限流、访问日志和链路沿用挂载它的路由上的中间件。
// Validate 为 nil、Accounts 为空或名称不合法时 panic。
func Handler(s *Server, opts HandlerOptions) http.Handler {
	switch {
	case s == nil:
		panic("mcpx: nil server")
	case opts.Validate == nil:
		panic("mcpx: HandlerOptions.Validate is required")
	case len(opts.Accounts) == 0:
		panic("mcpx: HandlerOptions.Accounts is required")
	}
	if err := opts.Accounts.Validate(); err != nil {
		panic(err)
	}
	challenge := "Bearer"
	if opts.Resource != nil {
		if err := opts.Resource.Validate(); err != nil {
			panic(err)
		}
		challenge = `Bearer resource_metadata="` + opts.Resource.MetadataURL() + `"`
	}
	sdk := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.sdk }, &mcp.StreamableHTTPOptions{
		Stateless:    true,
		JSONResponse: true,
		Logger:       s.sdkLogger(),
		// 防 DNS 重绑定的检查拒绝经本机地址到达、Host 却不是本机的请求，部署在反向代理之后时每个请求都会被拒绝。
		// 它保护的是没有认证、只监听本机的服务；这里每个请求都要带 Bearer token，不需要它。
		DisableLocalhostProtection: true,
	})
	permissions := append([]string(nil), opts.Permissions...)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ctx := r.Context()
		if opts.Tenant != nil {
			ctx = identity.WithTenant(ctx, opts.Tenant(r))
		}
		token := bearerToken(r.Header.Get("Authorization"))
		authenticate := func(ctx context.Context) (identity.Subject, error) {
			if token == "" {
				return identity.Subject{}, nil
			}
			return opts.Validate(ctx, token)
		}
		sub, err := identity.Establish(ctx, authenticate, opts.Accounts.Resolve, permissions...)
		switch {
		case err == nil:
		case identity.Failed(err):
			s.logger.ErrorContext(ctx, "authentication backend failed", "error", err)
			httpx.WriteProblem(w, httpx.StatusOf(err), "authentication unavailable")
			return
		case errors.Is(err, identity.ErrUnauthenticated):
			w.Header().Set("WWW-Authenticate", challenge)
			httpx.WriteProblem(w, http.StatusUnauthorized, httpx.ErrorDetail(ctx, err))
			return
		default:
			httpx.WriteProblem(w, httpx.StatusOf(err), httpx.ErrorDetail(ctx, err))
			return
		}
		sdk.ServeHTTP(w, r.WithContext(identity.WithSubject(ctx, sub)))
	})
}

// bearerToken 取出 Authorization: Bearer 后的 token，大小写不敏感；不是 Bearer 时返回空字符串。
func bearerToken(h string) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}
