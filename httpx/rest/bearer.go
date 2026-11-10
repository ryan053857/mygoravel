package rest

import (
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

// TokenValidator 校验 bearer token 并返回主体。
type TokenValidator func(ctx huma.Context, token string) (identity.Subject, error)

var (
	errUnauthenticated = errorx.Define(errorx.Unauthenticated, "auth.unauthenticated")
	errForbidden       = errorx.Define(errorx.PermissionDenied, "auth.forbidden")
)

// EnsureBearerScheme 在 OpenAPI 组件中登记 bearer 安全方案。
func EnsureBearerScheme(api huma.API) {
	oapi := api.OpenAPI()
	if oapi.Components == nil {
		oapi.Components = &huma.Components{}
	}
	if oapi.Components.SecuritySchemes == nil {
		oapi.Components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}
	oapi.Components.SecuritySchemes["bearer"] = &huma.SecurityScheme{
		Type:         "http",
		Scheme:       "bearer",
		BearerFormat: "opaque",
	}
}

// RequireBearer 同时设置 OpenAPI Security 与运行时中间件。
// 校验器返回有效主体，并满足 scopes 中的全部权限。
func RequireBearer(api huma.API, validate TokenValidator, scopes ...string) func(op *huma.Operation) {
	if validate == nil {
		panic("rest: nil token validator")
	}
	EnsureBearerScheme(api)
	scopeCopy := append([]string(nil), scopes...)
	return func(op *huma.Operation) {
		if op.Metadata == nil {
			op.Metadata = map[string]any{}
		}
		if op.Metadata["mygoravel.bearer"] == true {
			panic("rest: duplicate bearer declaration")
		}
		op.Metadata["mygoravel.bearer"] = true
		if len(scopeCopy) > 0 {
			if op.Extensions == nil {
				op.Extensions = map[string]any{}
			}
			op.Extensions["x-required-scopes"] = scopeCopy
		}
		op.Security = []map[string][]string{{"bearer": {}}}
		op.Middlewares = append(op.Middlewares, func(ctx huma.Context, next func(huma.Context)) {
			ctx.SetHeader("Cache-Control", "no-store")
			token := bearerToken(ctx.Header("Authorization"))
			if token == "" {
				ctx.SetHeader("WWW-Authenticate", `Bearer`)
				writeError(api, ctx, problem(api, ctx, errUnauthenticated.New()))
				return
			}
			sub, err := validate(ctx, token)
			if err != nil && HTTPStatus(err) >= 500 {
				if a, ok := api.(*API); ok {
					a.logger.ErrorContext(ctx.Context(), "authentication backend failed", "error", err)
				}
				writeError(api, ctx, huma.NewErrorWithContext(ctx, HTTPStatus(err), "authentication unavailable"))
				return
			}
			if err != nil || sub.ID == "" {
				ctx.SetHeader("WWW-Authenticate", `Bearer`)
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

// Public 显式标记端点为公开（无 Security）。
func Public() func(op *huma.Operation) {
	return func(op *huma.Operation) {
		op.Security = []map[string][]string{}
		if op.Metadata["mygoravel.bearer"] == true {
			panic("rest: cannot mark an authenticated endpoint public")
		}
	}
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
