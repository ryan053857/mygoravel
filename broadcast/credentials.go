package broadcast

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/identity"
)

// Credentials 通知所有实例上的长连接立即重新校验凭据，实现 httpx.CredentialWatcher。
// 把它设置为 rest.Options.Credentials 和 web.Options.Credentials，
// 撤销 token、退出会话、停用用户或修改权限之后调用对应的 Recheck 方法，受影响的 SSE 连接随即复查，
// 凭据已经失效的连接结束。
//
// 它只提醒复查，不说明凭据怎样变化：是否结束连接由认证中间件的复查决定，多发的提醒只是多查一次。
// 提醒丢失（Backend 断线、调用之前进程退出）时，连接仍由 30 秒一次的定期复查兜底。
//
// 提醒要在数据提交之后发出，否则复查读到的还是旧数据：撤销在事务中进行时，用 tx.AfterCommit 调用。
type Credentials struct {
	hub *Hub
}

// NewCredentials 创建基于 hub 的 Credentials。hub 为 nil 时 panic。
func NewCredentials(hub *Hub) *Credentials {
	if hub == nil {
		panic("broadcast: nil hub")
	}
	return &Credentials{hub: hub}
}

// 三种提醒的主题前缀。
const (
	credentialTopic = "cred.c."
	subjectTopic    = "cred.s."
	tenantTopic     = "cred.t."
)

// credTopic 返回提醒的主题。ID 中可能有主题不允许的字符，因此使用哈希。
func credTopic(prefix, id string) string {
	h := sha256.Sum256([]byte(id))
	return prefix + hex.EncodeToString(h[:16])
}

// RecheckCredential 让使用凭据 id 的长连接立即复查，id 是主体的 Credential：
// API 令牌的 ID 或会话 ID。用于退出某一台设备，例如 apitoken.Store.Revoke、session.Store.Revoke 之后。
func (c *Credentials) RecheckCredential(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("broadcast: empty credential id")
	}
	return c.hub.Publish(ctx, credTopic(credentialTopic, id), nil)
}

// RecheckSubject 让主体 ref 的全部长连接立即复查。用于退出其他设备、修改密码、停用账号、
// 移出租户或修改这个主体的角色之后。
func (c *Credentials) RecheckSubject(ctx context.Context, ref identity.Ref) error {
	if err := ref.Validate(); err != nil {
		return errors.Wrap(err, "broadcast")
	}
	return c.hub.Publish(ctx, credTopic(subjectTopic, ref.String()), nil)
}

// RecheckTenant 让请求所在租户为 tenant 的全部长连接立即复查，"" 是平台本身。
// 用于修改角色的权限等影响整个租户的变化之后。
func (c *Credentials) RecheckTenant(ctx context.Context, tenant string) error {
	return c.hub.Publish(ctx, credTopic(tenantTopic, tenant), nil)
}

// Watch 实现 httpx.CredentialWatcher：关注 sub.Credential、sub.Ref() 和 identity.TenantFromContext(ctx)，
// 收到对应的 Recheck 时向返回的通道发送信号，多个信号在读取前合并为一个。
// 订阅因为跟不上或 Backend 可能丢了消息而结束时，先发一个信号（期间的提醒可能丢了），再重新订阅。
// ctx 结束或 Hub 关闭后停止关注。
func (c *Credentials) Watch(ctx context.Context, sub identity.Subject) <-chan struct{} {
	topics := []string{credTopic(tenantTopic, identity.TenantFromContext(ctx))}
	if sub.ID != "" {
		topics = append(topics, credTopic(subjectTopic, sub.Ref().String()))
	}
	if sub.Credential != "" {
		topics = append(topics, credTopic(credentialTopic, sub.Credential))
	}
	ch := make(chan struct{}, 1)
	signal := func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	go func() {
		for {
			s := c.hub.Subscribe(topics...)
			if !forward(ctx, s, signal) {
				return
			}
			signal()
		}
	}()
	return ch
}

// forward 把 s 的每条消息转为一个信号，直到 ctx 结束或订阅结束。
// 返回 true 表示订阅意外结束（ErrSlow、ErrLost），应当重新订阅。
func forward(ctx context.Context, s *Subscription, signal func()) bool {
	defer s.Close()
	for {
		select {
		case <-ctx.Done():
			return false
		case _, ok := <-s.C():
			if !ok {
				return !errors.Is(s.Err(), ErrClosed)
			}
			signal()
		}
	}
}
