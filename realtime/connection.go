package realtime

import (
	"context"
	"net/http"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

// errUnauthenticated 表示 token 无效，只在包内用于归类，不会返回给客户端。
var errUnauthenticated = errorx.Define(errorx.Unauthenticated, "realtime.unauthenticated")

// connection 是一个连接的身份。
type connection struct {
	sub   identity.Subject // ID 为空表示匿名
	token string           // 由 Options.Authenticate 认证时的 token
}

type connectionKey struct{}

// connecting 识别连接的主体：认证中间件写入的主体优先，其次是 SDK 传入的 token，都没有时是匿名连接。
func (rt *Realtime) connecting(ctx context.Context, e centrifuge.ConnectEvent) (centrifuge.ConnectReply, error) {
	conn := &connection{}
	if sub, ok := identity.FromContext(ctx); ok && sub.ID != "" {
		conn.sub = sub
	} else if e.Token != "" {
		sub, err := rt.authenticate(ctx, e.Token)
		switch {
		case err != nil && httpx.StatusOf(err) >= http.StatusInternalServerError:
			rt.logger.ErrorContext(ctx, "authentication backend failed", "error", err)
			return centrifuge.ConnectReply{}, centrifuge.ErrorInternal
		case err != nil:
			return centrifuge.ConnectReply{}, centrifuge.DisconnectInvalidToken
		}
		conn.sub, conn.token = sub, e.Token
		ctx = identity.WithSubject(ctx, sub)
	}
	return centrifuge.ConnectReply{
		Context:     context.WithValue(ctx, connectionKey{}, conn),
		Credentials: &centrifuge.Credentials{UserID: conn.sub.ID},
	}, nil
}

// authenticate 用 Options.Authenticate 校验 token。返回的错误已归类：5xx 表示暂时无法判断，其他表示 token 无效。
func (rt *Realtime) authenticate(ctx context.Context, token string) (identity.Subject, error) {
	if rt.opts.Authenticate == nil {
		return identity.Subject{}, errUnauthenticated.New()
	}
	sub, err := rt.opts.Authenticate(ctx, token)
	switch {
	case err != nil && httpx.StatusOf(err) >= http.StatusInternalServerError:
		return identity.Subject{}, err
	case err != nil || sub.ID == "":
		return identity.Subject{}, errUnauthenticated.New()
	case sub.Tenant != "" && sub.Tenant != identity.TenantFromContext(ctx):
		return identity.Subject{}, errUnauthenticated.New()
	}
	return sub, nil
}

// valid 重新校验连接的凭据，只有确定已经失效时返回 false。
func (rt *Realtime) valid(ctx context.Context, conn *connection) bool {
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	if conn.token == "" {
		return httpx.CheckCredential(ctx) == nil
	}
	sub, err := rt.authenticate(ctx, conn.token)
	if err != nil {
		return httpx.StatusOf(err) >= http.StatusInternalServerError
	}
	return sub.ID == conn.sub.ID && sub.Impersonator == conn.sub.Impersonator
}

// connected 为已经建立的连接安装订阅和发布的处理器，并在连接期间复查凭据。
func (rt *Realtime) connected(client *centrifuge.Client) {
	conn, _ := client.Context().Value(connectionKey{}).(*connection)
	if conn == nil {
		client.Disconnect(centrifuge.DisconnectServerError)
		return
	}
	client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
		// 授权可能要查询数据库，不占用连接的读取循环。
		go func() { cb(centrifuge.SubscribeReply{}, rt.authorize(client.Context(), conn, e.Channel)) }()
	})
	client.OnPublish(func(e centrifuge.PublishEvent, cb centrifuge.PublishCallback) {
		rule, _, ok := rt.match(e.Channel)
		if !ok || !rule.publish || !client.IsSubscribed(e.Channel) {
			cb(centrifuge.PublishReply{}, centrifuge.ErrorPermissionDenied)
			return
		}
		cb(centrifuge.PublishReply{Options: centrifuge.PublishOptions{ClientInfo: e.ClientInfo}}, nil)
	})
	if conn.sub.ID != "" {
		go rt.watch(client, conn)
	}
}

// authorize 按频道规则决定连接能否订阅 channel，返回 nil 表示允许。
func (rt *Realtime) authorize(ctx context.Context, conn *connection, channel string) error {
	rule, params, ok := rt.match(channel)
	if !ok {
		return centrifuge.ErrorUnknownChannel
	}
	if rule.authorize == nil {
		return nil
	}
	if conn.sub.ID == "" {
		return centrifuge.ErrorPermissionDenied
	}
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	allowed, err := rule.authorize(ctx, conn.sub, params)
	switch {
	case err != nil && httpx.StatusOf(err) >= http.StatusInternalServerError:
		if ctx.Err() == nil || context.Cause(ctx) == context.DeadlineExceeded {
			rt.logger.ErrorContext(ctx, "channel authorization failed", "channel", channel, "error", err)
		}
		return centrifuge.ErrorInternal
	case err != nil || !allowed:
		return centrifuge.ErrorPermissionDenied
	}
	return nil
}

// watch 在连接期间定期复查凭据，收到 Options.Credentials 的信号时立即复查；凭据失效后断开连接，客户端不再重连。
func (rt *Realtime) watch(client *centrifuge.Client, conn *connection) {
	ctx := client.Context()
	var changed <-chan struct{}
	if rt.opts.Credentials != nil {
		changed = rt.opts.Credentials.Watch(ctx, conn.sub)
	}
	t := time.NewTicker(credentialInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case _, ok := <-changed:
			if !ok {
				changed = nil
				continue
			}
		}
		if !rt.valid(ctx, conn) {
			client.Disconnect(centrifuge.DisconnectInvalidToken)
			return
		}
	}
}
