package session

import (
	"context"
	"encoding/gob"
	"log/slog"
	"net/http"

	"github.com/alexedwards/scs/v2"
	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging"
)

// subjectKey 是会话中保存已认证主体的键。
const subjectKey = "mygoravel.subject"

func init() {
	// scs 默认用 gob 编码会话数据，接口值中的自定义类型需要注册。
	gob.Register(identity.Subject{})
}

// New 按 cfg 创建 scs 会话管理器，读写会话数据直接使用它的原生方法。
// store 是会话存储，例如 NewStore 或 scs 的 memstore；logger 记录会话存储错误，nil 时使用 slog.Default()。
// 会话加载或保存失败时返回统一格式的 500。cfg 不合法或 store 为 nil 时 panic。
//
// 返回值的 LoadAndSave 中间件需要安装在路由上，Login、Logout、Require 和 scs 的读写方法都依赖它。
func New(cfg Config, store scs.Store, logger *slog.Logger) *scs.SessionManager {
	if err := cfg.Validate(); err != nil {
		panic(errors.Wrap(err, "session"))
	}
	if store == nil {
		panic("session: nil store")
	}
	logger = logging.Or(logger).With(logging.AttrComponent, "session")
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
	sm.ErrorFunc = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.ErrorContext(r.Context(), "session store failed", "error", err)
		httpx.WriteProblem(w, http.StatusInternalServerError, http.StatusText(http.StatusInternalServerError))
	}
	return sm
}

// Login 把 sub 记为当前会话的主体，Method 置为 session。
// 它先更换会话 token，防止登录前泄露的 token 被沿用（会话固定攻击）。
func Login(ctx context.Context, sm *scs.SessionManager, sub identity.Subject) error {
	if sub.ID == "" {
		return errors.New("session: subject ID is required")
	}
	if err := sm.RenewToken(ctx); err != nil {
		return errors.Wrap(err, "session: renew token")
	}
	sub.Method = "session"
	sm.Put(ctx, subjectKey, sub)
	return nil
}

// Logout 销毁当前会话，并让浏览器删除会话 cookie。
func Logout(ctx context.Context, sm *scs.SessionManager) error {
	return errors.Wrap(sm.Destroy(ctx), "session: destroy")
}

// Subject 返回当前会话中已登录的主体。
// 受 Require 保护的路由应当使用 identity.FromContext 读取主体。
func Subject(ctx context.Context, sm *scs.SessionManager) (identity.Subject, bool) {
	sub, ok := sm.Get(ctx, subjectKey).(identity.Subject)
	return sub, ok && sub.ID != ""
}
