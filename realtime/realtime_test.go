package realtime_test

import (
	"context"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/broadcast/redisbroadcast"
	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/internal/redistest"
	"github.com/ryan053857/mygoravel/realtime"
)

var (
	errRevoked     = errorx.Define(errorx.Unauthenticated, "test.revoked")
	errUnavailable = errorx.Define(errorx.Unavailable, "test.unavailable")
)

// bus 是连接多个 Hub 的 Backend，模拟多实例共用的后端。
type bus struct {
	mu        sync.Mutex
	instances []*instance
}

type instance struct {
	bus     *bus
	ch      chan broadcast.Message
	lostCh  chan error
	started chan struct{}
}

func (b *bus) backend() *instance {
	i := &instance{bus: b, ch: make(chan broadcast.Message, 256), lostCh: make(chan error, 1), started: make(chan struct{})}
	b.mu.Lock()
	b.instances = append(b.instances, i)
	b.mu.Unlock()
	return i
}

func (i *instance) Publish(_ context.Context, msg broadcast.Message) error {
	i.bus.mu.Lock()
	defer i.bus.mu.Unlock()
	for _, other := range i.bus.instances {
		other.ch <- msg
	}
	return nil
}

func (i *instance) Listen(ctx context.Context, deliver func(broadcast.Message), lost func(error)) {
	close(i.started)
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-i.ch:
			deliver(msg)
		case err := <-i.lostCh:
			lost(err)
		}
	}
}

// node 是一个实例：Hub、Realtime 和它的 HTTP 服务器。
type node struct {
	hub *broadcast.Hub
	rt  *realtime.Realtime
	url string
}

// start 启动一个实例。wrap 包装 Handler，模拟认证等中间件；可以为 nil。
func start(t *testing.T, backend broadcast.Backend, opts realtime.Options, wrap func(http.Handler) http.Handler) *node {
	t.Helper()
	hub := broadcast.New(backend, nil)
	if opts.Authenticate != nil && opts.Accounts == nil {
		opts.Accounts = identity.Accounts{"user": nil}
	}
	rt, err := realtime.New(hub, opts)
	if err != nil {
		t.Fatal(err)
	}
	h := rt.Handler()
	if wrap != nil {
		h = wrap(h)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		defer cancel()
		_ = rt.Shutdown(ctx)
		srv.Close()
		_ = hub.Close()
	})
	return &node{hub: hub, rt: rt, url: srv.URL}
}

func publish(t *testing.T, hub *broadcast.Hub, topic string, v any) {
	t.Helper()
	if err := hub.PublishJSON(context.Background(), topic, v); err != nil {
		t.Fatal(err)
	}
}

// tokens 是测试用的 token 校验器：token 就是主体 ID，可以撤销、使其过期或模拟后端故障。
type tokens struct {
	mu      sync.Mutex
	revoked map[string]bool
	expired map[string]bool
	down    bool
}

func (s *tokens) validate(_ context.Context, token string) (identity.Subject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.down:
		return identity.Subject{}, errUnavailable.New()
	case s.revoked[token]:
		return identity.Subject{}, errRevoked.New()
	case s.expired[token]:
		return identity.Subject{}, errRevoked.Wrap(identity.ErrExpired.New())
	}
	return identity.Subject{Type: "user", ID: token, Credential: "cred-" + token}, nil
}

func (s *tokens) revoke(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked == nil {
		s.revoked = map[string]bool{}
	}
	s.revoked[token] = true
}

func (s *tokens) expire(token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.expired == nil {
		s.expired = map[string]bool{}
	}
	s.expired[token] = true
}

func (s *tokens) setDown(down bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.down = down
}

func TestHubPublishReachesClientsAndSubscribers(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{}, nil)
	n.rt.Channel("articles", realtime.Public())
	c := mustConnect(t, n.url, "", nil)
	c.mustSubscribe("articles")
	sub := n.hub.Subscribe("articles")
	defer sub.Close()

	publish(t, n.hub, "articles", map[string]string{"title": "hello"})
	if p := c.push(); p.Channel != "articles" || string(p.Pub.Data) != `{"title":"hello"}` || p.Pub.Info != nil {
		t.Fatalf("push = %+v", p)
	}
	if m := broadcasttest.Receive(t, sub); string(m.Data) != `{"title":"hello"}` {
		t.Fatalf("subscriber received %q", m.Data)
	}

	// 不是 JSON 的消息只交给 Hub 的订阅，不发给客户端。
	if err := n.hub.Publish(context.Background(), "articles", nil); err != nil {
		t.Fatal(err)
	}
	broadcasttest.Receive(t, sub)
	c.noPush(100 * time.Millisecond)
}

func TestAcrossInstances(t *testing.T) {
	b := &bus{}
	a := start(t, b.backend(), realtime.Options{}, nil)
	other := start(t, b.backend(), realtime.Options{}, nil)
	for _, n := range []*node{a, other} {
		n.rt.Channel("room:{id}", realtime.Public().ClientPublish())
	}
	ca, cb := mustConnect(t, a.url, "", nil), mustConnect(t, other.url, "", nil)
	ca.mustSubscribe("room:1")
	cb.mustSubscribe("room:1")
	sub := other.hub.Subscribe("room:1")
	defer sub.Close()

	// 在 a 上发布，两个实例上的客户端和 Hub 订阅都收到。
	publish(t, a.hub, "room:1", map[string]int{"n": 1})
	for _, c := range []*client{ca, cb} {
		if p := c.push(); p.Channel != "room:1" || string(p.Pub.Data) != `{"n":1}` {
			t.Fatalf("push = %+v", p)
		}
	}
	broadcasttest.Receive(t, sub)

	// 其他频道的消息不会发给它们。
	publish(t, a.hub, "room:2", map[string]int{"n": 2})
	cb.noPush(100 * time.Millisecond)

	// 客户端发布的消息带有发布者的信息，发给两个实例上的客户端，Hub 的订阅收不到。
	if reply := ca.publish("room:1", map[string]bool{"typing": true}); reply.Error != nil {
		t.Fatalf("client publish: %+v", reply.Error)
	}
	for _, c := range []*client{ca, cb} {
		p := c.push()
		if string(p.Pub.Data) != `{"typing":true}` || p.Pub.Info == nil || p.Pub.Info.Client == "" {
			t.Fatalf("client publication = %+v", p.Pub)
		}
	}
	select {
	case m := <-sub.C():
		t.Fatalf("hub subscriber received client publication %q", m.Data)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestUnsubscribeAcrossInstances(t *testing.T) {
	b := &bus{}
	store := &tokens{}
	a := start(t, b.backend(), realtime.Options{Authenticate: store.validate}, nil)
	other := start(t, b.backend(), realtime.Options{Authenticate: store.validate}, nil)
	other.rt.Channel("chat:{id}", realtime.Private(func(context.Context, identity.Subject, realtime.Params) (bool, error) {
		return true, nil
	}))
	alice, bob := mustConnect(t, other.url, "alice", nil), mustConnect(t, other.url, "bob", nil)
	for _, c := range []*client{alice, bob} {
		c.mustSubscribe("chat:1")
		c.mustSubscribe("chat:2")
	}

	// 在 a 上取消 alice 对 chat:1 的订阅，连接在另一个实例上。
	if err := a.rt.Unsubscribe(identity.Ref{Type: "user", ID: "alice"}, "chat:1"); err != nil {
		t.Fatal(err)
	}
	if ch := alice.unsubscribed(); ch != "chat:1" {
		t.Fatalf("unsubscribed from %q", ch)
	}
	publish(t, a.hub, "chat:1", map[string]int{"n": 1})
	publish(t, a.hub, "chat:2", map[string]int{"n": 2})
	if p := alice.push(); p.Channel != "chat:2" {
		t.Fatalf("alice received a push on %s", p.Channel)
	}
	if p := bob.push(); p.Channel != "chat:1" {
		t.Fatalf("bob received a push on %s first", p.Channel)
	}
	if err := a.rt.Unsubscribe(identity.Ref{}, "chat:1"); err == nil {
		t.Fatal("empty subject id accepted")
	}
}

func TestChannelRules(t *testing.T) {
	store := &tokens{}
	var down atomic.Bool
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate}, nil)
	n.rt.Channel("articles", realtime.Public())
	n.rt.Channel("chat:{conversation}", realtime.Private(func(ctx context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
		if got, ok := identity.FromContext(ctx); !ok || got.ID != sub.ID {
			t.Errorf("context subject = %+v, want %s", got, sub.ID)
		}
		if down.Load() {
			return false, errUnavailable.New()
		}
		return p["conversation"] == "of-"+sub.ID, nil
	}))

	anonymous := mustConnect(t, n.url, "", nil)
	anonymous.mustSubscribe("articles")
	for channel, code := range map[string]int{
		"chat:of-alice": 103, // 私有频道需要登录
		"unknown":       102, // 没有匹配的规则
		"cred.s.abc":    102, // Hub 的其他主题不会因此暴露
		"bad channel":   102,
	} {
		if reply := anonymous.subscribe(channel); reply.Error == nil || reply.Error.Code != code {
			t.Errorf("anonymous subscribe %q: %+v, want code %d", channel, reply.Error, code)
		}
	}

	alice := mustConnect(t, n.url, "alice", nil)
	alice.mustSubscribe("chat:of-alice")
	if reply := alice.subscribe("chat:of-bob"); reply.Error == nil || reply.Error.Code != 103 {
		t.Fatalf("subscribe to another user's channel: %+v", reply.Error)
	}
	publish(t, n.hub, "chat:of-alice", map[string]string{"text": "hi"})
	if p := alice.push(); p.Channel != "chat:of-alice" {
		t.Fatalf("push = %+v", p)
	}
	anonymous.noPush(100 * time.Millisecond)

	// 授权暂时无法判断时返回内部错误，客户端 SDK 会稍后重新订阅。
	down.Store(true)
	bob := mustConnect(t, n.url, "bob", nil)
	if reply := bob.subscribe("chat:of-bob"); reply.Error == nil || reply.Error.Code != 100 {
		t.Fatalf("subscribe while authorizer is down: %+v", reply.Error)
	}
}

func TestClientPublishNeedsRuleAndSubscription(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{}, nil)
	n.rt.Channel("articles", realtime.Public())
	n.rt.Channel("typing:{room}", realtime.Public().ClientPublish())
	c := mustConnect(t, n.url, "", nil)
	c.mustSubscribe("articles")
	for _, channel := range []string{"articles", "typing:1", "unknown"} {
		if reply := c.publish(channel, map[string]int{"n": 1}); reply.Error == nil || reply.Error.Code != 103 {
			t.Errorf("publish to %q: %+v, want permission denied", channel, reply.Error)
		}
	}
	c.mustSubscribe("typing:1")
	if reply := c.publish("typing:1", map[string]int{"n": 1}); reply.Error != nil {
		t.Fatalf("publish after subscribing: %+v", reply.Error)
	}
	c.push()
}

// Options.Accounts 之外的账号体系的 token 被拒绝，与无效 token 相同。
func TestTokenAccounts(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{
		Authenticate: (&tokens{}).validate, // 给出 user 类型的主体
		Accounts:     identity.Accounts{"customer": nil},
	}, nil)
	if rejected(t, n.url, "alice", nil) != 3500 {
		t.Fatal("token of another account type was accepted")
	}
}

func TestAccountsRequired(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	t.Cleanup(func() { _ = hub.Close() })
	defer func() {
		if recover() == nil {
			t.Fatal("Authenticate without Accounts: expected panic")
		}
	}()
	_, _ = realtime.New(hub, realtime.Options{Authenticate: (&tokens{}).validate})
}

func TestTokenAuthentication(t *testing.T) {
	store := &tokens{}
	store.revoke("revoked")
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate}, nil)

	if rejected(t, n.url, "revoked", nil) != 3500 {
		t.Fatal("revoked token was not rejected as invalid")
	}
	// 只是过期的 token 返回“token 已过期”（109），连接不关闭：SDK 换新的 token 后重试。
	store.expire("expired")
	if _, reply := connect(t, n.url, "expired", nil); reply.Error == nil || reply.Error.Code != 109 {
		t.Fatalf("connect with an expired token: %+v", reply)
	}
	// 后端故障时返回内部错误，不是无效 token：客户端 SDK 会稍后重连。
	store.setDown(true)
	if _, reply := connect(t, n.url, "alice", nil); reply.Error == nil || reply.Error.Code != 100 {
		t.Fatalf("connect while backend is down: %+v", reply)
	}
	store.setDown(false)
	mustConnect(t, n.url, "alice", nil)

	// 没有配置 Authenticate 时，带 token 的连接一律拒绝。
	plain := start(t, broadcasttest.NewBackend(), realtime.Options{}, nil)
	if rejected(t, plain.url, "alice", nil) != 3500 {
		t.Fatal("token was accepted without Authenticate")
	}
}

func TestTokenBoundToAnotherTenantIsRejected(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{
		Authenticate: func(context.Context, string) (identity.Subject, error) {
			return identity.Subject{Type: "user", ID: "alice", Tenant: "acme"}, nil
		},
	}, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(identity.WithTenant(r.Context(), r.Header.Get("X-Tenant"))))
		})
	})
	if rejected(t, n.url, "t", http.Header{"X-Tenant": {"globex"}}) != 3500 {
		t.Fatal("token of another tenant was accepted")
	}
	mustConnect(t, n.url, "t", http.Header{"X-Tenant": {"acme"}})
}

// Resolve 加载 token 连接的权限：频道的授权函数看到的是 Resolve 之后的主体，凭据字段仍然取自 token；
// Resolve 拒绝时连接不成立，暂时故障时客户端稍后重连。
func TestResolveTokenSubject(t *testing.T) {
	var (
		mu    sync.Mutex
		state = "member"
	)
	set := func(v string) { mu.Lock(); state = v; mu.Unlock() }
	n := start(t, broadcasttest.NewBackend(), realtime.Options{
		Authenticate: (&tokens{}).validate,
		Accounts: identity.Accounts{"user": func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			mu.Lock()
			defer mu.Unlock()
			switch state {
			case "down":
				return identity.Subject{}, errUnavailable.New()
			case "stranger":
				return identity.Subject{}, errorx.Define(errorx.NotFound, "test.not_member").New()
			}
			// 重新构造主体：凭据字段不由 Resolve 决定。
			return identity.Subject{Type: sub.Type, ID: sub.ID, Permissions: []string{"articles:read"}}, nil
		}},
	}, nil)
	seen := make(chan identity.Subject, 1)
	n.rt.Channel("articles", realtime.Private(func(_ context.Context, sub identity.Subject, _ realtime.Params) (bool, error) {
		seen <- sub
		return sub.Can("articles:read"), nil
	}))
	mustConnect(t, n.url, "alice", nil).mustSubscribe("articles")
	if sub := <-seen; sub.Credential != "cred-alice" || !sub.Can("articles:read") {
		t.Fatalf("subject %+v", sub)
	}

	set("stranger")
	if rejected(t, n.url, "alice", nil) != 3500 {
		t.Fatal("subject rejected by Resolve was connected")
	}
	set("down")
	if _, reply := connect(t, n.url, "alice", nil); reply.Error == nil || reply.Error.Code != 100 {
		t.Fatalf("connect while Resolve is down: %+v", reply)
	}
}

// 连接期间 Resolve 与 Authenticate 一起复查：用户被移出租户后连接结束，Resolve 暂时故障时保持。
func TestResolveRecheckDisconnects(t *testing.T) {
	defer realtime.SetCredentialInterval(50 * time.Millisecond)()
	var (
		mu    sync.Mutex
		state = "member"
	)
	set := func(v string) { mu.Lock(); state = v; mu.Unlock() }
	n := start(t, broadcasttest.NewBackend(), realtime.Options{
		Authenticate: (&tokens{}).validate,
		Accounts: identity.Accounts{"user": func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
			mu.Lock()
			defer mu.Unlock()
			switch state {
			case "down":
				return identity.Subject{}, errUnavailable.New()
			case "removed":
				return identity.Subject{}, errorx.Define(errorx.NotFound, "test.not_member").New()
			}
			return sub, nil
		}},
	}, nil)
	c := mustConnect(t, n.url, "alice", nil)
	set("down")
	time.Sleep(150 * time.Millisecond)
	n.rt.Channel("articles", realtime.Public())
	c.mustSubscribe("articles")
	set("removed")
	if code := c.closed(); code != 3500 {
		t.Fatalf("closed with %d", code)
	}
}

// Options.Tenant 把连接所在的租户写入 context：Resolve 按它加载权限，租户频道的授权函数看到的主体带着这个租户，
// 绑定了其他租户的 token 被拒绝。
func TestTenantOption(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{
		Authenticate: func(_ context.Context, token string) (identity.Subject, error) {
			bound, _ := strings.CutPrefix(token, "bound-to-")
			if bound == token {
				bound = "" // 没有绑定租户的 token
			}
			return identity.Subject{Type: "user", ID: "alice", Tenant: bound}, nil
		},
		Accounts: identity.Accounts{"user": func(ctx context.Context, sub identity.Subject) (identity.Subject, error) {
			sub.Tenant, sub.Permissions = identity.TenantFromContext(ctx), []string{"articles"}
			return sub, nil
		}},
		Tenant: func(r *http.Request) string { return r.Header.Get("X-Tenant") },
	}, nil)
	n.rt.Channel("t:{tenant}:articles", realtime.Private(func(_ context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
		return p["tenant"] == sub.Tenant && sub.Can("articles"), nil
	}))
	acme := http.Header{"X-Tenant": {"acme"}}
	c := mustConnect(t, n.url, "any", acme)
	c.mustSubscribe("t:acme:articles")
	if reply := c.subscribe("t:globex:articles"); reply.Error == nil || reply.Error.Code != 103 {
		t.Fatalf("subscribed to another tenant's channel: %+v", reply)
	}
	mustConnect(t, n.url, "bound-to-acme", acme)
	if rejected(t, n.url, "bound-to-globex", acme) != 3500 {
		t.Fatal("token bound to another tenant was accepted")
	}
}

func TestRevokedTokenDisconnects(t *testing.T) {
	store := &tokens{}
	// 复查信号经由另一个 Hub 传递也可以：Credentials 只负责提醒。
	signals := broadcast.New(broadcasttest.NewBackend(), nil)
	defer signals.Close()
	creds := broadcast.NewCredentials(signals)
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate, Credentials: creds}, nil)
	alice, bob := mustConnect(t, n.url, "alice", nil), mustConnect(t, n.url, "bob", nil)

	// 后端故障时的复查不会断开连接。
	store.setDown(true)
	if err := creds.RecheckSubject(context.Background(), identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	store.setDown(false)

	store.revoke("alice")
	if err := creds.RecheckCredential(context.Background(), "cred-alice"); err != nil {
		t.Fatal(err)
	}
	if code := alice.closed(); code != 3500 {
		t.Fatalf("revoked connection closed with %d", code)
	}
	// 其他连接不受影响。
	n.rt.Channel("articles", realtime.Public())
	bob.mustSubscribe("articles")
}

func TestPeriodicRecheckDisconnects(t *testing.T) {
	defer realtime.SetCredentialInterval(50 * time.Millisecond)()
	store := &tokens{}
	n := start(t, broadcasttest.NewBackend(), realtime.Options{Authenticate: store.validate}, nil)
	c := mustConnect(t, n.url, "alice", nil)
	time.Sleep(150 * time.Millisecond) // 有效的凭据经过几次复查仍然保持连接
	n.rt.Channel("articles", realtime.Public())
	c.mustSubscribe("articles")
	store.revoke("alice")
	if code := c.closed(); code != 3500 {
		t.Fatalf("closed with %d", code)
	}
	// token 在连接期间过期：以 3005 断开，SDK 重连时会收到 109，换新的 token。
	b := mustConnect(t, n.url, "bob", nil)
	store.expire("bob")
	if code := b.closed(); code != 3005 {
		t.Fatalf("expired token closed with %d", code)
	}
}

// wrapped 是只提供 Unwrap 的 ResponseWriter，与框架的中间件相同。
type wrapped struct{ http.ResponseWriter }

func (w wrapped) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestBehindMiddleware(t *testing.T) {
	defer realtime.SetCredentialInterval(50 * time.Millisecond)()
	var mu sync.Mutex
	loggedOut := false
	// 模拟页面路由的认证中间件：写入主体和凭据复查，包装 ResponseWriter，并设置很短的请求超时。
	auth := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := identity.WithTenant(r.Context(), "acme")
			ctx = identity.WithSubject(ctx, identity.Subject{Type: "user", ID: "alice", Tenant: "acme", Method: "session"})
			ctx = httpx.WithCredentialCheck(ctx, func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				if loggedOut {
					return errRevoked.New()
				}
				return nil
			})
			next.ServeHTTP(wrapped{w}, r.WithContext(ctx))
		})
	}
	timeout := httpx.Timeout(httpx.TimeoutConfig{Duration: 100 * time.Millisecond})
	n := start(t, broadcasttest.NewBackend(), realtime.Options{
		// 中间件已经识别出主体时不使用 token。
		Authenticate: func(context.Context, string) (identity.Subject, error) {
			t.Error("Authenticate called for a request that already has a subject")
			return identity.Subject{}, errRevoked.New()
		},
	}, func(next http.Handler) http.Handler { return timeout(auth(next)) })
	n.rt.Channel("t:{tenant}:inbox", realtime.Private(func(ctx context.Context, sub identity.Subject, p realtime.Params) (bool, error) {
		return sub.ID == "alice" && p["tenant"] == sub.Tenant && identity.TenantFromContext(ctx) == "acme", nil
	}))

	c := mustConnect(t, n.url, "ignored", nil)
	time.Sleep(300 * time.Millisecond) // 超过请求超时，连接仍然可用
	c.mustSubscribe("t:acme:inbox")
	if reply := c.subscribe("t:globex:inbox"); reply.Error == nil || reply.Error.Code != 103 {
		t.Fatalf("subscribe to another tenant: %+v", reply.Error)
	}
	publish(t, n.hub, "t:acme:inbox", map[string]int{"unread": 3})
	c.push()

	mu.Lock()
	loggedOut = true
	mu.Unlock()
	if code := c.closed(); code != 3500 {
		t.Fatalf("closed with %d", code)
	}
}

func TestOrigin(t *testing.T) {
	n := start(t, broadcasttest.NewBackend(), realtime.Options{TrustedOrigins: []string{"https://app.example.com"}}, nil)
	if _, resp, err := dial(t, n.url, http.Header{"Origin": {"https://evil.example.com"}}); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin connection: err=%v resp=%v", err, resp)
	}
	for _, origin := range []string{"https://app.example.com", n.url} {
		mustConnect(t, n.url, "", http.Header{"Origin": {origin}})
	}
	defer func() {
		if recover() == nil {
			t.Fatal("malformed trusted origin did not panic")
		}
	}()
	_, _ = realtime.New(n.hub, realtime.Options{TrustedOrigins: []string{"app.example.com"}})
}

func TestLostMessagesDisconnectClients(t *testing.T) {
	b := &bus{}
	backend := b.backend()
	n := start(t, backend, realtime.Options{}, nil)
	n.rt.Channel("articles", realtime.Public())
	c := mustConnect(t, n.url, "", nil)
	c.mustSubscribe("articles")
	<-backend.started
	backend.lostCh <- context.DeadlineExceeded
	// 3010 让 SDK 重连并重新订阅，应用随后取回期间的数据。
	if code := c.closed(); code != 3010 {
		t.Fatalf("closed with %d", code)
	}
	fresh := mustConnect(t, n.url, "", nil)
	fresh.mustSubscribe("articles")
	publish(t, n.hub, "articles", map[string]int{"n": 1})
	fresh.push()
}

func TestShutdownDisconnectsClients(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	defer hub.Close()
	rt, err := realtime.New(hub, realtime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(rt.Handler())
	defer srv.Close()
	c := mustConnect(t, srv.URL, "", nil)
	if err := rt.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 3001 是可重连的关闭码，客户端会连到其他实例。
	if code := c.closed(); code != 3001 {
		t.Fatalf("closed with %d", code)
	}
	// 停止之后到达的连接同样被要求重连。
	if code := rejected(t, srv.URL, "", nil); code != 3001 {
		t.Fatalf("connection after shutdown closed with %d", code)
	}
}

func TestChannelPatterns(t *testing.T) {
	match, err := realtime.Compile("t:{tenant}:chat.{conversation}")
	if err != nil {
		t.Fatal(err)
	}
	if p, ok := match("t:acme:chat.0198-ab_c"); !ok || p["tenant"] != "acme" || p["conversation"] != "0198-ab_c" {
		t.Fatalf("params = %v ok = %v", p, ok)
	}
	for _, channel := range []string{"t:acme:chat.", "t:acme:chat.a.b", "t:a:b:chat.c", "x:acme:chat.c", "t:acme:chat.c:more", ""} {
		if p, ok := match(channel); ok {
			t.Errorf("%q matched with %v", channel, p)
		}
	}
	if match, _ := realtime.Compile("articles"); func() bool { _, ok := match("articles"); return !ok }() {
		t.Error("literal pattern did not match itself")
	}
	for _, pattern := range []string{"", "has space", "a..b", "{a}:{a}", "chat:{", "chat:{1x}"} {
		if _, err := realtime.Compile(pattern); err == nil {
			t.Errorf("pattern %q compiled", pattern)
		}
	}
}

func TestAcrossInstancesOnRedis(t *testing.T) {
	rdb, channel := redistest.Standalone.Open(t), "test:realtime:"+rand.Text()
	a := start(t, redisbroadcast.New(rdb, channel), realtime.Options{}, nil)
	b := start(t, redisbroadcast.New(rdb, channel), realtime.Options{}, nil)
	broadcasttest.Ready(t, a.hub, b.hub)
	for _, n := range []*node{a, b} {
		n.rt.Channel("room:{id}", realtime.Public().ClientPublish())
	}
	ca, cb := mustConnect(t, a.url, "", nil), mustConnect(t, b.url, "", nil)
	ca.mustSubscribe("room:1")
	cb.mustSubscribe("room:1")

	// 不同实例发布的消息没有确定的先后，所以等前一条到达之后再发布下一条。
	publish(t, a.hub, "room:1", map[string]int{"n": 1})
	for _, c := range []*client{ca, cb} {
		if p := c.push(); string(p.Pub.Data) != `{"n":1}` || p.Pub.Info != nil {
			t.Fatalf("first push = %+v", p.Pub)
		}
	}
	if reply := cb.publish("room:1", map[string]bool{"typing": true}); reply.Error != nil {
		t.Fatalf("client publish: %+v", reply.Error)
	}
	for _, c := range []*client{ca, cb} {
		if p := c.push(); string(p.Pub.Data) != `{"typing":true}` || p.Pub.Info == nil {
			t.Fatalf("second push = %+v", p.Pub)
		}
	}
}

func TestMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	n := start(t, broadcasttest.NewBackend(), realtime.Options{MeterProvider: sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))}, nil)
	n.rt.Channel("room:{id}", realtime.Public())
	c := mustConnect(t, n.url, "", nil)
	mustConnect(t, n.url, "", nil)
	c.mustSubscribe("room:1")
	c.mustSubscribe("room:2")
	c.mustSubscribe("room:3")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			got[m.Name] = m.Data.(metricdata.Gauge[int64]).DataPoints[0].Value
		}
	}
	if got["realtime.connections"] != 2 || got["realtime.subscriptions"] != 3 {
		t.Fatalf("metrics = %v", got)
	}
}
