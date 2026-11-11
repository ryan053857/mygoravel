package httpx

import (
	"context"
	"net/http"

	"github.com/ryan053857/mygoravel/identity"
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

// CredentialWatcher 在凭据可能已经变化时通知长连接立即重新校验，不必等下一次定期校验。
// broadcast.Credentials 是基于广播的实现，多个实例之间也能互相通知。
type CredentialWatcher interface {
	// Watch 返回一个通道：sub 所用的凭据（sub.Credential）、sub 本身（sub.ID），
	// 或请求所在的租户（identity.TenantFromContext）被要求复查时，通道收到信号。ctx 结束时停止关注。
	Watch(ctx context.Context, sub identity.Subject) <-chan struct{}
}

type watcherKey struct{}

// WithCredentialWatcher 返回带有 w 的 context。认证中间件在安装 CredentialCheck 的同时调用它，
// rest.Options.Credentials 和 web.Options.Credentials 设置后自动安装。
// SSE 开始时用它关注请求的主体，收到信号就立即执行 CheckCredential。
func WithCredentialWatcher(ctx context.Context, w CredentialWatcher) context.Context {
	return context.WithValue(ctx, watcherKey{}, w)
}

// watchCredential 关注 ctx 中主体的凭据，没有 CredentialWatcher 或主体时返回 nil。
func watchCredential(ctx context.Context) <-chan struct{} {
	w, _ := ctx.Value(watcherKey{}).(CredentialWatcher)
	sub, ok := identity.FromContext(ctx)
	if w == nil || !ok {
		return nil
	}
	return w.Watch(ctx, sub)
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
