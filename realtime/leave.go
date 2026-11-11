package realtime

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/centrifugal/centrifuge"
)

// DefaultPresenceGrace 是 Options.PresenceGrace 为零时的宽限期。
const DefaultPresenceGrace = 10 * time.Second

// MaxPresenceGrace 是宽限期的上限。在线记录的有效期是 60 秒、每 25 秒续期一次，
// 连接离开时它的记录至少还有 35 秒有效；宽限期更长的话，记录会先于宽限期过期。
const MaxPresenceGrace = 30 * time.Second

// membership 决定 presence 频道的加入和离开事件何时发出，让事件按主体而不是按连接：
//
//   - 主体已经有其他连接在频道中时，新连接的加入不发事件；
//   - 连接离开后在线记录保留一个宽限期，期满时主体没有其他连接才发出离开事件。
//
// 所以网络闪断后在宽限期内重连、刷新页面、多开标签页，其他成员都看不到离开和加入。
// 「主体有没有其他连接」读取的是所有实例共用的 PresenceStore，重连到另一个实例也一样。
type membership struct {
	grace  time.Duration // <= 0 表示没有宽限期
	store  PresenceStore
	send   func(envelope) error
	logger *slog.Logger

	mu      sync.Mutex
	pending map[leaveKey]*pendingLeave
	closed  bool
}

type leaveKey struct{ channel, client string }

// pendingLeave 是一个已经离开、还在宽限期内的连接。
type pendingLeave struct {
	timer    *time.Timer
	info     *centrifuge.ClientInfo // 离开事件的内容；Centrifuge 没有要求发出离开事件时为 nil
	rejoined bool                   // 同一个连接已经重新订阅，等它的加入事件到来时一并取消
}

func newMembership(grace time.Duration, store PresenceStore, send func(envelope) error, logger *slog.Logger) *membership {
	if grace == 0 {
		grace = DefaultPresenceGrace
	}
	return &membership{grace: grace, store: store, send: send, logger: logger, pending: map[leaveKey]*pendingLeave{}}
}

// remove 在连接 client 离开 channel 时调用：有宽限期时在线记录保留到期满，否则立即删除。
func (m *membership) remove(channel, client string) error {
	if m.grace <= 0 {
		return m.delete(channel, client)
	}
	m.wait(leaveKey{channel, client}, nil)
	return nil
}

// leave 在 Centrifuge 要求发出离开事件时调用，紧接在 remove 之后。
func (m *membership) leave(channel string, info *centrifuge.ClientInfo) error {
	if m.grace <= 0 {
		return m.announce(channel, info)
	}
	m.wait(leaveKey{channel, info.ClientID}, info)
	return nil
}

// wait 登记或更新 key 的宽限期。Realtime 已经停止时什么也不做，在线记录自行过期。
func (m *membership) wait(key leaveKey, info *centrifuge.ClientInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	p := m.pending[key]
	if p == nil || p.rejoined {
		if p != nil {
			p.timer.Stop()
		}
		p = &pendingLeave{}
		p.timer = time.AfterFunc(m.grace, func() { m.expire(key, p) })
		m.pending[key] = p
	}
	if info != nil {
		p.info = info
	}
}

// expire 在宽限期满时删除在线记录，主体没有其他连接时发出离开事件。
// 记录已经不在时不发事件：它被 expel 删除了，离开事件当时已经发出。
func (m *membership) expire(key leaveKey, p *pendingLeave) {
	m.mu.Lock()
	if m.closed || m.pending[key] != p || p.rejoined {
		m.mu.Unlock()
		return
	}
	delete(m.pending, key)
	info := p.info
	m.mu.Unlock()

	entries, err := m.list(key.channel)
	if err != nil {
		m.logger.Error("list presence", "channel", key.channel, "error", err)
	}
	listed, others := false, false
	for _, e := range entries {
		switch {
		case e.Client == key.client:
			listed = true
		case info != nil && e.Subject == info.UserID:
			others = true
		}
	}
	if err == nil && !listed {
		return
	}
	if err := m.delete(key.channel, key.client); err != nil {
		m.logger.Error("remove presence", "channel", key.channel, "error", err)
	}
	if info == nil || others {
		return
	}
	if err := m.send(envelope{Kind: kindLeave, Channel: key.channel, Info: info}); err != nil {
		m.logger.Error("publish leave", "channel", key.channel, "error", err)
	}
}

// expel 立即删除主体在 channel 中的全部在线记录并发出离开事件，不经过宽限期。
// 用于 Realtime.Unsubscribe：主体被移出频道，不会再回来。连接所在的实例稍后宽限期满时，
// 发现记录已经不在，不再重复发出离开事件。
func (m *membership) expel(channel, subject string) error {
	entries, err := m.list(channel)
	if err != nil {
		return err
	}
	var info *centrifuge.ClientInfo
	for _, e := range entries {
		if e.Subject != subject {
			continue
		}
		if err := m.delete(channel, e.Client); err != nil {
			return err
		}
		if info == nil {
			info = &centrifuge.ClientInfo{ClientID: e.Client, UserID: e.Subject, ChanInfo: e.Info}
		}
	}
	if info == nil {
		return nil
	}
	return m.send(envelope{Kind: kindLeave, Channel: channel, Info: info})
}

// refreshed 在连接 client 写入或续期在线记录时调用：同一个连接在宽限期内重新订阅了，取消待发的离开。
func (m *membership) refreshed(channel, client string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.pending[leaveKey{channel, client}]; p != nil && !p.rejoined {
		p.timer.Stop()
		p.rejoined = true
	}
}

// join 在 Centrifuge 要求发出加入事件时调用。其他成员没有看到这个主体离开时不发事件：
// 同一个连接在宽限期内重新订阅，或者主体还有其他连接的在线记录（包括仍在宽限期内的）。
func (m *membership) join(channel string, info *centrifuge.ClientInfo) error {
	key := leaveKey{channel, info.ClientID}
	m.mu.Lock()
	p := m.pending[key]
	if p != nil {
		p.timer.Stop()
		delete(m.pending, key)
	}
	m.mu.Unlock()
	if p != nil || m.present(channel, info) {
		return nil
	}
	return m.send(envelope{Kind: kindJoin, Channel: channel, Info: info})
}

// announce 发出离开事件，主体还有其他连接在频道中时不发。
func (m *membership) announce(channel string, info *centrifuge.ClientInfo) error {
	if m.present(channel, info) {
		return nil
	}
	return m.send(envelope{Kind: kindLeave, Channel: channel, Info: info})
}

// present 报告主体在 channel 中是否有 info.ClientID 之外的在线记录。读取失败时按没有处理：
// 多发一次事件比漏发好，客户端按主体合并。
func (m *membership) present(channel string, info *centrifuge.ClientInfo) bool {
	entries, err := m.list(channel)
	if err != nil {
		m.logger.Error("list presence", "channel", channel, "error", err)
		return false
	}
	for _, e := range entries {
		if e.Subject == info.UserID && e.Client != info.ClientID {
			return true
		}
	}
	return false
}

func (m *membership) list(channel string) ([]PresenceEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), presenceTimeout)
	defer cancel()
	return m.store.List(ctx, channel)
}

func (m *membership) delete(channel, client string) error {
	ctx, cancel := context.WithTimeout(context.Background(), presenceTimeout)
	defer cancel()
	return m.store.Remove(ctx, channel, client)
}

// close 取消全部待发的离开：实例停止时不发离开事件，在线记录自行过期，
// 客户端重连到其他实例时其他成员看不到离开和加入。
func (m *membership) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	for key, p := range m.pending {
		p.timer.Stop()
		delete(m.pending, key)
	}
}
