package session

import (
	"context"
	"encoding/gob"
	"time"

	"github.com/alexedwards/scs/v2"
	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/identity"
)

const (
	// subjectKey 是会话中保存已认证主体的键。
	subjectKey = "mygoravel.subject"
	// loginKey 是会话中保存登录设备的键，Store 把它写入设备信息列。
	loginKey = "mygoravel.login"
	// confirmedKey 是会话中保存最近一次确认身份的时间（Unix 毫秒）的键。
	confirmedKey = "mygoravel.confirmed_at"
)

// maxUserAgent 是保存的 User-Agent 的最大字符数，与 MySQL 迁移中的列宽一致。
const maxUserAgent = 255

func init() {
	// scs 默认用 gob 编码会话数据，接口值中的自定义类型需要注册。
	gob.Register(identity.Subject{})
	gob.Register(login{})
}

// Device 描述登录所用的设备，用于设备列表。
type Device struct {
	// IP 是登录请求的客户端地址。
	IP string
	// UserAgent 是登录请求的 User-Agent，超过 255 个字符时截断。
	UserAgent string
}

// login 是保存在会话数据中的登录设备和时间。
type login struct {
	IP        string
	UserAgent string
	At        time.Time
}

// New 按 cfg 创建 scs 会话管理器，读写会话数据直接使用它的原生方法。
// store 是会话存储，例如 NewStore 或 scs 的 memstore。cfg 不合法或 store 为 nil 时 panic。
//
// 会话用于服务端渲染的页面：把返回值交给 web.New，由 Pages.Middleware 加载和保存会话，
// 并把会话存储的错误呈现为错误页。Login、Logout 和 scs 的读写方法都依赖这个中间件。
func New(cfg Config, store scs.Store) *scs.SessionManager {
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "session"))
	}
	if store == nil {
		panic("session: nil store")
	}
	sameSite, _ := cfg.Cookie.sameSite()

	sm := scs.New()
	sm.Store = store
	sm.Lifetime = cfg.Lifetime
	sm.IdleTimeout = cfg.IdleTimeout
	sm.Cookie = scs.SessionCookie{
		Name:     cfg.Cookie.Name,
		Domain:   cfg.Cookie.Domain,
		Path:     cfg.Cookie.Path,
		HttpOnly: true,
		Secure:   cfg.Cookie.Secure,
		SameSite: sameSite,
		Persist:  cfg.Cookie.Persist,
	}
	return sm
}

// Login 把 sub 记为当前会话的主体，并记录登录设备 d。
// 它先更换会话 token，防止登录前泄露的 token 被沿用（会话固定攻击）。
//
// 每次登录得到一个新的会话 ID，保存为主体的 Credential（Method 为 session），
// 与 API 令牌的 ID 一样用于设备列表中标出当前设备、退出其他设备，见 Store.List。
func Login(ctx context.Context, sm *scs.SessionManager, sub identity.Subject, d Device) error {
	if err := sub.Ref().Validate(); err != nil {
		return errors.Wrap(err, "session")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return errors.Wrap(err, "session: new id")
	}
	if err := sm.RenewToken(ctx); err != nil {
		return errors.Wrap(err, "session: renew token")
	}
	sub.Method, sub.Credential = "session", id.String()
	sm.Put(ctx, subjectKey, sub)
	now := time.Now().UTC()
	sm.Put(ctx, loginKey, login{IP: d.IP, UserAgent: truncate(d.UserAgent, maxUserAgent), At: now})
	sm.Put(ctx, confirmedKey, now.UnixMilli())
	return nil
}

// Confirm 记录当前会话的主体此刻再次确认了身份，例如在修改邮箱、关闭二次验证之前重新输入了密码。
// 登录本身也算一次确认。见 ConfirmedAt。
func Confirm(ctx context.Context, sm *scs.SessionManager) {
	sm.Put(ctx, confirmedKey, time.Now().UnixMilli())
}

// ConfirmedAt 返回当前会话最近一次确认身份的时间（登录或 Confirm）；未登录时返回零值。
func ConfirmedAt(ctx context.Context, sm *scs.SessionManager) time.Time {
	ms, ok := sm.Get(ctx, confirmedKey).(int64)
	if !ok {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// Logout 销毁当前会话，并让浏览器删除会话 cookie。
func Logout(ctx context.Context, sm *scs.SessionManager) error {
	return errors.Wrap(sm.Destroy(ctx), "session: destroy")
}

// Lookup 直接从会话存储读取 token 对应的会话，返回其中已登录的主体，不使用请求中已经加载的会话数据。
// 会话不存在、已过期或已撤销时 ok 为 false。用于在长连接期间确认会话仍然有效。
func Lookup(ctx context.Context, sm *scs.SessionManager, token string) (sub identity.Subject, ok bool, err error) {
	if token == "" {
		return identity.Subject{}, false, nil
	}
	var (
		b     []byte
		found bool
	)
	if cs, isCtx := sm.Store.(scs.CtxStore); isCtx {
		b, found, err = cs.FindCtx(ctx, token)
	} else {
		b, found, err = sm.Store.Find(token)
	}
	if err != nil || !found {
		return identity.Subject{}, false, errors.Wrap(err, "session: lookup")
	}
	deadline, values, err := sm.Codec.Decode(b)
	if err != nil {
		return identity.Subject{}, false, errors.Wrap(err, "session: decode")
	}
	if !time.Now().Before(deadline) {
		return identity.Subject{}, false, nil
	}
	sub, ok = values[subjectKey].(identity.Subject)
	return sub, ok && sub.ID != "", nil
}

// Subject 返回当前会话中已登录的主体。
// 受 web.Pages.Require 保护的路由应当使用 identity.FromContext 读取主体。
func Subject(ctx context.Context, sm *scs.SessionManager) (identity.Subject, bool) {
	sub, ok := sm.Get(ctx, subjectKey).(identity.Subject)
	return sub, ok && sub.ID != ""
}
