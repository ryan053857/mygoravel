package httpx

import (
	"context"
	"net/http"
)

// CredentialCheck 在长连接期间重新校验请求的凭据。
//
// 凭据已经失效（会话或 token 被撤销、用户被停用、失去所需的 scope）时返回 4xx 类错误，
// 例如 errorx.Unauthenticated、errorx.PermissionDenied；暂时无法判断（例如数据库故障）时
// 返回 5xx 类错误，连接保持，下次再查。
type CredentialCheck func(ctx context.Context) error

type credentialKey struct{}

// WithCredentialCheck 返回带有 check 的 context。认证中间件在认证通过后调用它，
// SSE 等长连接据此定期重新校验，凭据失效后结束连接。rest.RequireAuth 和 web 的页面路由都已安装。
func WithCredentialCheck(ctx context.Context, check CredentialCheck) context.Context {
	checks, _ := ctx.Value(credentialKey{}).([]CredentialCheck)
	return context.WithValue(ctx, credentialKey{}, append(checks[:len(checks):len(checks)], check))
}

// CheckCredential 运行 ctx 中的全部 CredentialCheck，返回第一个表示凭据失效（4xx 类）的错误；
// 没有校验、全部通过，或只是暂时无法判断时返回 nil。
func CheckCredential(ctx context.Context) error {
	checks, _ := ctx.Value(credentialKey{}).([]CredentialCheck)
	for _, check := range checks {
		if err := check(ctx); err != nil && StatusOf(err) < http.StatusInternalServerError {
			return err
		}
	}
	return nil
}
