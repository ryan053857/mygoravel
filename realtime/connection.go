package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
)

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
		sub, err := identity.Establish(ctx, rt.authenticator(e.Token), rt.opts.Accounts.Resolve)
		switch {
		case identity.Failed(err):
			rt.logger.ErrorContext(ctx, "authentication backend failed", "error", err)
			return centrifuge.ConnectReply{}, centrifuge.ErrorInternal
		case errors.Is(err, identity.ErrExpired):
			// SDK 收到后调用 getToken 换新的 token 再连接，例如 JWT 模式的 access token 在断线期间过期。
			return centrifuge.ConnectReply{}, centrifuge.ErrorTokenExpired
		case err != nil:
			return centrifuge.ConnectReply{}, centrifuge.DisconnectInvalidToken
		}
		conn.sub, conn.token = sub, e.Token
		ctx = identity.WithSubject(ctx, sub)
	}
	return centrifuge.ConnectReply{
		Context:     context.WithValue(ctx, connectionKey{}, conn),
		Credentials: &centrifuge.Credentials{UserID: conn.sub.Ref().String()},
	}, nil
}

// authenticator 用 Options.Authenticate 校验 token；没有设置时带 token 的连接一律无效。
func (rt *Realtime) authenticator(token string) identity.Authenticator {
	return func(ctx context.Context) (identity.Subject, error) {
		if rt.opts.Authenticate == nil {
			return identity.Subject{}, nil
		}
		return rt.opts.Authenticate(ctx, token)
	}
}

// check 重新校验连接的凭据，只有确定已经失效时返回断开的方式：token 只是过期时以 DisconnectExpired 断开，
// SDK 重连时收到“token 已过期”，换新的 token；其他情况以 DisconnectInvalidToken 断开，SDK 不再重连。
func (rt *Realtime) check(ctx context.Context, conn *connection) *centrifuge.Disconnect {
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	var err error
	if conn.token == "" {
		err = httpx.CheckCredential(ctx)
	} else {
		err = identity.Reestablish(ctx, conn.sub, rt.authenticator(conn.token), rt.opts.Accounts.Resolve)
	}
	switch {
	case err == nil, identity.Failed(err):
		return nil
	case errors.Is(err, identity.ErrExpired):
		return &centrifuge.DisconnectExpired
	default:
		return &centrifuge.DisconnectInvalidToken
	}
}

// connected 为已经建立的连接安装订阅和发布的处理器，并在连接期间复查凭据。
func (rt *Realtime) connected(client *centrifuge.Client) {
	conn, _ := client.Context().Value(connectionKey{}).(*connection)
	if conn == nil {
		client.Disconnect(centrifuge.DisconnectServerError)
		return
	}
	client.OnSubscribe(func(e centrifuge.SubscribeEvent, cb centrifuge.SubscribeCallback) {
		// 在连接的读取循环中同步授权：Centrifuge 先写出订阅的回复、再把频道标记为已订阅，
		// 异步回调时客户端收到回复后立即发布或读取成员，会赶在标记之前而被拒绝。
		opts, err := rt.authorize(client.Context(), conn, e.Channel)
		cb(centrifuge.SubscribeReply{Options: opts}, err)
	})
	client.OnPublish(func(e centrifuge.PublishEvent, cb centrifuge.PublishCallback) {
		rule, _, ok := rt.match(e.Channel)
		if !ok || !rule.publish || !client.IsSubscribed(e.Channel) {
			cb(centrifuge.PublishReply{}, centrifuge.ErrorPermissionDenied)
			return
		}
		cb(centrifuge.PublishReply{Options: centrifuge.PublishOptions{ClientInfo: e.ClientInfo}}, nil)
	})
	// 只有 presence 频道的订阅者可以读取成员列表；结果留空时由 Centrifuge 从 PresenceStore 读取。
	member := func(channel string) error {
		if rule, _, ok := rt.match(channel); !ok || rule.presence == nil || !client.IsSubscribed(channel) {
			return centrifuge.ErrorPermissionDenied
		}
		return nil
	}
	client.OnPresence(func(e centrifuge.PresenceEvent, cb centrifuge.PresenceCallback) {
		cb(centrifuge.PresenceReply{}, member(e.Channel))
	})
	client.OnPresenceStats(func(e centrifuge.PresenceStatsEvent, cb centrifuge.PresenceStatsCallback) {
		cb(centrifuge.PresenceStatsReply{}, member(e.Channel))
	})
	if conn.sub.ID != "" {
		go rt.watch(client, conn)
	}
}

// authorize 按频道规则决定连接能否订阅 channel，返回订阅的选项；错误为 nil 表示允许。
func (rt *Realtime) authorize(ctx context.Context, conn *connection, channel string) (centrifuge.SubscribeOptions, error) {
	var opts centrifuge.SubscribeOptions
	rule, params, ok := rt.match(channel)
	if !ok {
		return opts, centrifuge.ErrorUnknownChannel
	}
	if rule.authorize == nil && rule.presence == nil {
		return opts, nil
	}
	if conn.sub.ID == "" {
		return opts, centrifuge.ErrorPermissionDenied
	}
	ctx, cancel := context.WithTimeout(ctx, credentialTimeout)
	defer cancel()
	var (
		info    any
		allowed bool
		err     error
	)
	if rule.presence != nil {
		info, allowed, err = rule.presence(ctx, conn.sub, params)
	} else {
		allowed, err = rule.authorize(ctx, conn.sub, params)
	}
	switch {
	case err != nil && httpx.StatusOf(err) >= http.StatusInternalServerError:
		if ctx.Err() == nil || context.Cause(ctx) == context.DeadlineExceeded {
			rt.logger.ErrorContext(ctx, "channel authorization failed", "channel", channel, "error", err)
		}
		return opts, centrifuge.ErrorInternal
	case err != nil || !allowed:
		return opts, centrifuge.ErrorPermissionDenied
	}
	if rule.presence != nil {
		opts.EmitPresence, opts.EmitJoinLeave, opts.PushJoinLeave = true, true, true
		if info != nil {
			if opts.ChannelInfo, err = json.Marshal(info); err != nil {
				rt.logger.ErrorContext(ctx, "encode presence info", "channel", channel, "error", err)
				return opts, centrifuge.ErrorInternal
			}
		}
	}
	return opts, nil
}

// watch 在连接期间定期复查凭据，收到 Options.Credentials 的信号时立即复查；凭据失效后断开连接，见 check。
func (rt *Realtime) watch(client *centrifuge.Client, conn *connection) {
	ctx := client.Context()
	var changed <-chan struct{}
	if rt.opts.Credentials != nil {
		changed = rt.opts.Credentials.Watch(ctx, conn.sub)
	}
	t := time.NewTicker(rt.recheck)
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
		if d := rt.check(ctx, conn); d != nil {
			client.Disconnect(*d)
			return
		}
	}
}
