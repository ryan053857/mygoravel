package realtime

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/cockroachdb/errors"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/metric"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
)

// credentialInterval 是连接期间重新校验凭据的间隔，与 SSE 相同。
var credentialInterval = 30 * time.Second

// credentialTimeout 是一次凭据校验或订阅授权的时限。
const credentialTimeout = 10 * time.Second

// Options 配置 Realtime，零值可用。
type Options struct {
	// Authenticate 校验客户端 SDK 连接时传入的 token，返回主体，通常与 rest.RequireBearer 使用同一个校验器。
	// SPA 和移动端用它认证：浏览器的 WebSocket 不能设置 Authorization 请求头。ctx 是连接请求的 context。
	// 返回 4xx 类错误或 ID 为空的主体表示 token 无效；返回 5xx 类错误表示暂时无法判断，客户端稍后重连。
	// 连接期间用同一个 token 定期重新调用，token 被撤销后连接结束。
	//
	// nil 时带 token 的连接一律拒绝。请求已经由认证中间件识别出主体时（例如同源页面的会话），不使用 token。
	Authenticate func(ctx context.Context, token string) (identity.Subject, error)
	// Resolve 把 Authenticate 给出的主体转换为这个连接的主体，例如按 identity.TenantFromContext 加载用户在租户中的权限，
	// 通常与 rest.Options.Resolve 相同（authz.Store.Resolve）；nil 时直接使用 Authenticate 给出的主体。
	// 返回 errorx.Unauthenticated 类错误或 ID 为空的主体表示主体已失效，其他 4xx 类错误同样拒绝连接；
	// 连接期间与 Authenticate 一起定期重新调用，用户被停用或移出租户后连接结束。
	// 认证中间件识别出的主体（例如同源页面的会话）已经由 web.Options.Resolve 处理，不再经过它。
	Resolve identity.Resolver
	// Tenant 从连接请求中取出它所在的租户，例如路径参数 chi.URLParam(r, "tenant")、子域名或请求头，
	// 结果写入 identity.WithTenant：Resolve 据此加载权限，绑定了其他租户的 token 被拒绝。
	// 与 rest.Options.Tenant、web.Options.Tenant 的作用相同。nil 时使用外层中间件已经写入的租户
	// （页面路由由 web.Options.Tenant 写入），都没有时是平台本身（""）。
	Tenant func(r *http.Request) string
	// Credentials 让连接在收到复查信号时立即重新校验凭据，撤销 token、退出登录后连接随即结束；
	// nil 时只按 30 秒的间隔定期校验。通常是 broadcast.NewCredentials 的返回值，
	// 与 rest.Options.Credentials、web.Options.Credentials 相同。
	Credentials httpx.CredentialWatcher
	// TrustedOrigins 是允许跨站建立连接的来源，例如 https://app.example.com。
	// 浏览器发起的连接默认只接受同源请求，没有 Origin 请求头的客户端（移动端、服务器）不受限制。
	TrustedOrigins []string
	// Presence 保存 presence 频道的在线成员，所有实例共用同一个存储：NewDatabasePresence 或 redispresence.New。
	// nil 时保存在进程内存中，只适合单实例部署。
	Presence PresenceStore
	// PresenceGrace 是连接离开 presence 频道后保留在线记录的时间：期间主体重新加入时，
	// 其他成员看不到离开和加入。零值为 DefaultPresenceGrace（10 秒），负数表示不保留，
	// 最长 MaxPresenceGrace（30 秒）。
	PresenceGrace time.Duration
	// MeterProvider 非 nil 时记录 realtime.connections 和 realtime.subscriptions 两个指标。
	MeterProvider metric.MeterProvider
	// Logger 记录连接和授权中的错误；nil 表示 slog.Default()。
	Logger *slog.Logger
}

// Realtime 是嵌入的 Centrifuge 节点。每个进程创建一个，可以并发使用。
type Realtime struct {
	opts     Options
	hub      *broadcast.Hub
	node     *centrifuge.Node
	logger   *slog.Logger
	origins  map[string]struct{}
	presence PresenceStore
	members  *membership
	recheck  time.Duration // 凭据复查的间隔，创建时取自 credentialInterval
	stop     func()
	metrics  metric.Registration

	mu    sync.RWMutex
	rules []channelRule
}

// New 创建 Realtime 并开始把 hub 收到的消息转给客户端，直到 Shutdown。hub 由应用创建和关闭，
// 所有实例必须使用相同的后端。hub 为 nil、TrustedOrigins 格式不正确或 PresenceGrace 超过上限时 panic。
func New(hub *broadcast.Hub, opts Options) (*Realtime, error) {
	if hub == nil {
		panic("realtime: nil hub")
	}
	rt := &Realtime{
		opts:    opts,
		hub:     hub,
		logger:  logging.Or(opts.Logger).With(logging.AttrComponent, "realtime"),
		origins: map[string]struct{}{},
		recheck: credentialInterval,
	}
	if opts.PresenceGrace > MaxPresenceGrace {
		panic("realtime: PresenceGrace is longer than MaxPresenceGrace")
	}
	if rt.presence = opts.Presence; rt.presence == nil {
		rt.presence = newMemoryPresence()
	}
	for _, origin := range opts.TrustedOrigins {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme == "" || u.Host == "" || origin != u.Scheme+"://"+u.Host {
			panic("realtime: trusted origin must look like https://app.example.com, got " + origin)
		}
		rt.origins[strings.ToLower(origin)] = struct{}{}
	}
	node, err := centrifuge.New(centrifuge.Config{
		LogLevel:   centrifuge.LogLevelInfo,
		LogHandler: rt.log,
		// Centrifuge 默认把指标注册到 Prometheus 的全局 registry，这里给它一个独立的。
		Metrics: centrifuge.MetricsConfig{RegistererGatherer: prometheus.NewRegistry()},
	})
	if err != nil {
		return nil, errors.Wrap(err, "realtime: create node")
	}
	rt.node = node
	b := newBridge(hub, node)
	node.SetBroker(b)
	node.SetController(b)
	b.members = newMembership(opts.PresenceGrace, rt.presence, b.sendNow, rt.logger)
	rt.members = b.members
	node.SetPresenceManager(presenceManager{store: rt.presence, members: b.members})
	node.OnConnecting(rt.connecting)
	node.OnConnect(rt.connected)
	if err := node.Run(); err != nil {
		return nil, errors.Wrap(err, "realtime: run node")
	}
	go b.run()
	unlisten := hub.Listen(b.deliver, b.lost)
	rt.stop = func() {
		b.members.close()
		unlisten()
		b.stop()
	}
	if opts.MeterProvider != nil {
		if rt.metrics, err = rt.observe(opts.MeterProvider); err != nil {
			_ = rt.Shutdown(context.Background())
			return nil, err
		}
	}
	return rt, nil
}

// Channel 登记频道规则。pattern 是频道名，可以带 {name} 占位符，例如 chat:{conversation}；
// 占位符匹配一段字母、数字、_ 和 -，匹配到的值交给规则的 Authorizer。
// 订阅时使用第一条匹配的规则，没有匹配规则的频道不能订阅。
//
// 频道就是 Hub 的主题：登记规则之后，发布到同名主题的消息会发给订阅它的客户端。
// 多租户应用的频道名要带上租户，并在 Authorizer 中与主体的租户比较。pattern 不合法时 panic。
func (rt *Realtime) Channel(pattern string, rule Rule) {
	c, err := compile(pattern, rule)
	if err != nil {
		panic(err)
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.rules = append(rt.rules, c)
}

func (rt *Realtime) match(channel string) (Rule, Params, bool) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	for _, c := range rt.rules {
		if p, ok := c.match(channel); ok {
			return c.rule, p, true
		}
	}
	return Rule{}, nil, false
}

// Unsubscribe 取消主体 subjectID 在所有实例上对 channel 的订阅，用于主体失去访问权限之后，
// 例如被移出会话。客户端收到通知后不会自动重新订阅。presence 频道中，其他成员立即收到离开事件，
// 不经过 Options.PresenceGrace。它不参与数据库事务，在业务数据提交之后调用。
func (rt *Realtime) Unsubscribe(subjectID, channel string) error {
	if subjectID == "" {
		return errors.New("realtime: empty subject id")
	}
	if err := rt.node.Unsubscribe(subjectID, channel); err != nil {
		return errors.Wrap(err, "realtime: unsubscribe")
	}
	return errors.Wrap(rt.members.expel(channel, subjectID), "realtime: remove presence")
}

// Handler 返回接受 WebSocket 连接的处理器，客户端 SDK 连接到它所在的地址。
//
// 把它放在认证中间件之后时（例如 web 的页面路由），连接使用中间件识别出的主体；
// 否则由 Options.Authenticate 校验 SDK 传入的 token。两者都没有的连接是匿名的，只能订阅 Public 频道。
// 连接不受 Timeout 中间件的超时和 http.Server.WriteTimeout 影响。
func (rt *Realtime) Handler() http.Handler {
	ws := centrifuge.NewWebsocketHandler(rt.node, centrifuge.WebsocketConfig{CheckOrigin: rt.checkOrigin})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenant := rt.opts.Tenant; tenant != nil {
			r = r.WithContext(identity.WithTenant(r.Context(), tenant(r)))
		}
		ws.ServeHTTP(hijackable(w), r)
	})
}

// hijackable 沿着 Unwrap 找到实现 http.Hijacker 的 ResponseWriter：
// Centrifuge 直接断言 http.Hijacker，中间件包装过的 ResponseWriter 通常只提供 Unwrap。
func hijackable(w http.ResponseWriter) http.ResponseWriter {
	for cur := w; ; {
		if _, ok := cur.(http.Hijacker); ok {
			return cur
		}
		u, ok := cur.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return w
		}
		cur = u.Unwrap()
	}
}

func (rt *Realtime) checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	_, ok := rt.origins[strings.ToLower(u.Scheme+"://"+u.Host)]
	return ok
}

// Shutdown 断开全部客户端并停止节点，客户端会重连到其他实例。在关闭 Hub 之前调用。
func (rt *Realtime) Shutdown(ctx context.Context) error {
	if rt.metrics != nil {
		_ = rt.metrics.Unregister()
	}
	err := rt.node.Shutdown(ctx)
	rt.stop()
	return errors.Wrap(err, "realtime: shutdown")
}

// log 把 Centrifuge 的日志写到 slog。
func (rt *Realtime) log(e centrifuge.LogEntry) {
	// Centrifuge 的 Info 记录的是被拒绝的订阅、客户端断开这类日常事件，按 Debug 输出。
	level := slog.LevelDebug
	switch e.Level {
	case centrifuge.LogLevelWarn:
		level = slog.LevelWarn
	case centrifuge.LogLevelError:
		level = slog.LevelError
	}
	attrs := make([]any, 0, len(e.Fields)*2)
	for k, v := range e.Fields {
		attrs = append(attrs, k, v)
	}
	rt.logger.Log(context.Background(), level, e.Message, attrs...)
}
