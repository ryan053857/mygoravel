package rest

import (
	"context"
	"errors"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

// TokenValidator 校验 bearer token 并返回主体。ctx 是请求的 context。
type TokenValidator func(ctx context.Context, token string) (identity.Subject, error)

// Authenticator 从请求中识别调用者，供 RequireAuth 使用。
// 请求没有携带凭据时返回 ID 为空的主体或 errorx.Unauthenticated 错误；后端故障时返回 5xx 类错误。
type Authenticator func(ctx huma.Context) (identity.Subject, error)

const authMetadata = "mygoravel.auth"

// RequireBearer 要求请求携带有效的 Authorization: Bearer token，校验器返回有效主体，
// 并且主体能执行 permissions 中的全部权限（identity.Subject.Can）。
func RequireBearer(api huma.API, validate TokenValidator, permissions ...string) func(op *huma.Operation) {
	if validate == nil {
		panic("rest: nil token validator")
	}
	scheme := &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "opaque"}
	return RequireAuth(api, "bearer", scheme, func(ctx huma.Context) (identity.Subject, error) {
		token := bearerToken(ctx.Header("Authorization"))
		if token == "" {
			return identity.Subject{}, nil
		}
		return validate(ctx.Context(), token)
	}, permissions...)
}

// RequireAuth 为路由声明名为 name 的安全方案：在 OpenAPI 组件中登记 scheme 并标记路由的 Security，
// 同时安装运行时中间件。
//
// 运行时用 identity.Establish 得到本次请求的主体并写入 identity.WithSubject：
//
//   - authenticate 识别调用者：没有凭据或凭据无效返回 401，认证后端故障返回对应的 5xx；
//   - 主体的 Tenant 由凭据设置且与本次请求的租户（identity.TenantFromContext）不同时返回 403；
//   - 调用 Options.Resolve（如果有）得到本次请求的主体，错误的处理见 Options.Resolve；
//   - 主体不能执行 permissions 中的某项权限时返回 403。
//
// 认证通过后安装 httpx.CredentialCheck：SSE 等长连接期间定期重新执行以上检查（identity.Reestablish），
// 凭据失效或失去权限时结束连接。
// 每条路由只能声明一种认证方案。RequireBearer 基于它实现。
func RequireAuth(api huma.API, name string, scheme *huma.SecurityScheme, authenticate Authenticator, permissions ...string) func(op *huma.Operation) {
	if name == "" || scheme == nil || authenticate == nil {
		panic("rest: RequireAuth requires a scheme name, a security scheme and an authenticator")
	}
	registerScheme(api, name, scheme)
	challenge := ""
	if scheme.Type == "http" && scheme.Scheme != "" {
		challenge = strings.ToUpper(scheme.Scheme[:1]) + scheme.Scheme[1:]
	}
	required := append([]string(nil), permissions...)
	var resolve identity.Resolver
	var watcher httpx.CredentialWatcher
	if a, ok := api.(*API); ok {
		resolve, watcher = a.resolve, a.watcher
	}
	return func(op *huma.Operation) {
		if op.Metadata == nil {
			op.Metadata = map[string]any{}
		}
		if _, ok := op.Metadata[authMetadata]; ok {
			panic("rest: duplicate authentication declaration")
		}
		op.Metadata[authMetadata] = name
		if len(required) > 0 {
			if op.Extensions == nil {
				op.Extensions = map[string]any{}
			}
			op.Extensions["x-required-permissions"] = required
		}
		op.Security = []map[string][]string{{name: {}}}
		op.Middlewares = append(op.Middlewares, func(ctx huma.Context, next func(huma.Context)) {
			ctx.SetHeader("Cache-Control", "no-store")
			// authenticate 读取请求头等与 context 无关的部分，复查时只替换 context。
			auth := func(c context.Context) (identity.Subject, error) {
				return authenticate(huma.WithContext(ctx, c))
			}
			sub, err := identity.Establish(ctx.Context(), auth, resolve, required...)
			switch {
			case err == nil:
			case identity.Failed(err):
				if a, ok := api.(*API); ok {
					a.logger.ErrorContext(ctx.Context(), "authentication backend failed", "scheme", name, "error", err)
				}
				writeError(api, ctx, huma.NewErrorWithContext(ctx, httpx.StatusOf(err), "authentication unavailable"))
				return
			case errors.Is(err, identity.ErrUnauthenticated):
				if challenge != "" {
					ctx.SetHeader("WWW-Authenticate", challenge)
				}
				writeError(api, ctx, problem(api, ctx, err))
				return
			default:
				writeError(api, ctx, problem(api, ctx, err))
				return
			}
			// 长连接期间（例如 SSE）重新执行全部检查：凭据被撤销、主体变化、不再是租户成员或失去权限时结束连接。
			recheck := func(c context.Context) error {
				return identity.Reestablish(c, sub, auth, resolve, required...)
			}
			reqCtx := httpx.WithCredentialCheck(identity.WithSubject(ctx.Context(), sub), recheck)
			if watcher != nil {
				reqCtx = httpx.WithCredentialWatcher(reqCtx, watcher)
			}
			next(huma.WithContext(ctx, reqCtx))
		})
	}
}

// registerScheme 在 OpenAPI 组件中登记安全方案。
func registerScheme(api huma.API, name string, scheme *huma.SecurityScheme) {
	oapi := api.OpenAPI()
	if oapi.Components == nil {
		oapi.Components = &huma.Components{}
	}
	if oapi.Components.SecuritySchemes == nil {
		oapi.Components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}
	oapi.Components.SecuritySchemes[name] = scheme
}

// Public 显式标记端点为公开（无 Security）。
func Public() func(op *huma.Operation) {
	return func(op *huma.Operation) {
		op.Security = []map[string][]string{}
		if _, ok := op.Metadata[authMetadata]; ok {
			panic("rest: cannot mark an authenticated endpoint public")
		}
	}
}

// authScheme 返回 RequireAuth 为路由声明的方案名；声明缺失或 Security 被改动时返回空字符串。
func authScheme(op *huma.Operation) string {
	name, _ := op.Metadata[authMetadata].(string)
	if name == "" || len(op.Security) != 1 || len(op.Security[0]) != 1 {
		return ""
	}
	if _, ok := op.Security[0][name]; !ok {
		return ""
	}
	return name
}

// problem 转换中间件中的业务错误；api 为 *API 时按请求语言翻译 detail。
func problem(api huma.API, ctx huma.Context, err error) huma.StatusError {
	if a, ok := api.(*API); ok {
		return a.httpError(ctx.Context(), err)
	}
	return FromError(err)
}

func bearerToken(h string) string {
	const p = "Bearer "
	if len(h) < len(p) || !strings.EqualFold(h[:len(p)], p) {
		return ""
	}
	return strings.TrimSpace(h[len(p):])
}
