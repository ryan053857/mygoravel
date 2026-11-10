package rest

import (
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

// TokenValidator 校验 bearer token 并返回主体。
type TokenValidator func(ctx huma.Context, token string) (identity.Subject, error)

// Authenticator 从请求中识别调用者，供 RequireAuth 使用。
// 请求没有携带凭据时返回 ID 为空的主体或 errorx.Unauthenticated 错误；后端故障时返回 5xx 类错误。
type Authenticator func(ctx huma.Context) (identity.Subject, error)

const authMetadata = "mygoravel.auth"

var (
	errUnauthenticated = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")
	errForbidden       = errorx.Define(errorx.PermissionDenied, "auth.forbidden")
)

// RequireBearer 要求请求携带有效的 Authorization: Bearer token，
// 校验器返回有效主体，并满足 scopes 中的全部权限。
func RequireBearer(api huma.API, validate TokenValidator, scopes ...string) func(op *huma.Operation) {
	if validate == nil {
		panic("rest: nil token validator")
	}
	scheme := &huma.SecurityScheme{Type: "http", Scheme: "bearer", BearerFormat: "opaque"}
	return RequireAuth(api, "bearer", scheme, func(ctx huma.Context) (identity.Subject, error) {
		token := bearerToken(ctx.Header("Authorization"))
		if token == "" {
			return identity.Subject{}, nil
		}
		return validate(ctx, token)
	}, scopes...)
}

// RequireAuth 为路由声明名为 name 的安全方案：在 OpenAPI 组件中登记 scheme 并标记路由的 Security，
// 同时安装运行时中间件。authenticate 返回的主体满足 scopes 中的全部权限后写入 identity.WithSubject；
// 没有凭据返回 401，权限不足返回 403，认证后端故障返回对应的 5xx。
// 每条路由只能声明一种认证方案。RequireBearer 和 session.Require 都基于它实现。
func RequireAuth(api huma.API, name string, scheme *huma.SecurityScheme, authenticate Authenticator, scopes ...string) func(op *huma.Operation) {
	if name == "" || scheme == nil || authenticate == nil {
		panic("rest: RequireAuth requires a scheme name, a security scheme and an authenticator")
	}
	registerScheme(api, name, scheme)
	challenge := ""
	if scheme.Type == "http" && scheme.Scheme != "" {
		challenge = strings.ToUpper(scheme.Scheme[:1]) + scheme.Scheme[1:]
	}
	scopeCopy := append([]string(nil), scopes...)
	return func(op *huma.Operation) {
		if op.Metadata == nil {
			op.Metadata = map[string]any{}
		}
		if _, ok := op.Metadata[authMetadata]; ok {
			panic("rest: duplicate authentication declaration")
		}
		op.Metadata[authMetadata] = name
		if len(scopeCopy) > 0 {
			if op.Extensions == nil {
				op.Extensions = map[string]any{}
			}
			op.Extensions["x-required-scopes"] = scopeCopy
		}
		op.Security = []map[string][]string{{name: {}}}
		op.Middlewares = append(op.Middlewares, func(ctx huma.Context, next func(huma.Context)) {
			ctx.SetHeader("Cache-Control", "no-store")
			sub, err := authenticate(ctx)
			if err != nil && httpx.StatusOf(err) >= 500 {
				if a, ok := api.(*API); ok {
					a.logger.ErrorContext(ctx.Context(), "authentication backend failed", "scheme", name, "error", err)
				}
				writeError(api, ctx, huma.NewErrorWithContext(ctx, httpx.StatusOf(err), "authentication unavailable"))
				return
			}
			if err != nil || sub.ID == "" {
				if challenge != "" {
					ctx.SetHeader("WWW-Authenticate", challenge)
				}
				writeError(api, ctx, problem(api, ctx, errUnauthenticated.New()))
				return
			}
			for _, sc := range scopeCopy {
				if !sub.HasScope(sc) {
					writeError(api, ctx, problem(api, ctx, errForbidden.New(errorx.Public("scope", sc))))
					return
				}
			}
			next(huma.WithContext(ctx, identity.WithSubject(ctx.Context(), sub)))
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
