package realtime

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/centrifugal/centrifuge"
	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/identity"
)

// presenceTTL 是一条在线记录的有效期。连接每 25 秒续期一次，
// 实例崩溃后它上面的成员最多在这段时间之后从在线列表中消失。
const presenceTTL = 60 * time.Second

// presenceTimeout 是一次读写 PresenceStore 的时限。
const presenceTimeout = 5 * time.Second

// PresenceAuthorizer 决定 sub 能否加入 presence 频道。info 是其他成员能看到的资料，例如名字和头像，
// 会编码为 JSON，nil 表示没有资料；不要放入不该公开的字段。错误的含义与 Authorizer 相同。
type PresenceAuthorizer func(ctx context.Context, sub identity.Subject, p Params) (info any, allowed bool, err error)

// Presence 返回 presence 频道的规则：与 Private 一样只有 authorize 同意的主体可以订阅，
// 此外订阅者是频道的在线成员，彼此可以看到：客户端 SDK 能取得成员列表，并在成员加入、离开时收到事件。
// 服务端用 Realtime.Members 查询。authorize 为 nil 时 panic。
//
// 加入和离开事件按主体发出：主体的第一个连接加入时发出加入事件，之后的连接（例如另一个标签页）不再发；
// 连接离开后在线记录保留 Options.PresenceGrace，期满时主体没有其他连接才发出离开事件。
// 所以网络闪断后重连、刷新页面，其他成员都看不到离开和加入。客户端主动取消订阅同样经过宽限期；Realtime.Unsubscribe 把主体移出频道，离开事件立即发出。
//
// 事件可能重复（例如两个连接同时加入），客户端按主体 ID 处理。实例停止或崩溃时不发离开事件：
// 重连到其他实例的成员保持在线，没有重连的成员在一分钟内从列表中消失，页面应当定期读取成员列表。
func Presence(authorize PresenceAuthorizer) Rule {
	if authorize == nil {
		panic("realtime: nil presence authorizer")
	}
	return Rule{presence: authorize}
}

// PresenceEntry 是一个连接在一个频道中的在线记录。
type PresenceEntry struct {
	Channel string
	Client  string // 连接的 ID
	Subject identity.Ref
	Info    []byte // 成员资料的 JSON，可以为空
}

// PresenceStore 保存 presence 频道的在线记录，所有实例共用。实现必须可以并发使用。
// DatabasePresence 和 redispresence.Store 是两种实现。
type PresenceStore interface {
	// Add 写入或续期 e.Channel 中连接 e.Client 的记录，ttl 之后过期。
	Add(ctx context.Context, e PresenceEntry, ttl time.Duration) error
	// Remove 删除 channel 中连接 client 的记录，不存在时什么也不做。
	Remove(ctx context.Context, channel, client string) error
	// List 返回 channel 中没有过期的记录。
	List(ctx context.Context, channel string) ([]PresenceEntry, error)
}

// Member 是 presence 频道中的一个在线成员。
type Member struct {
	Subject     identity.Ref
	Info        json.RawMessage // 加入时频道规则给出的资料，没有时为 nil
	Connections int             // 这个主体在频道中的连接数
}

// Members 返回 presence 频道 channel 当前的在线成员，同一个主体的多个连接合并为一个，按主体的类型和 ID 排序。
// 多个连接的资料不同时取其中任意一个。
func (rt *Realtime) Members(ctx context.Context, channel string) ([]Member, error) {
	entries, err := rt.presence.List(ctx, channel)
	if err != nil {
		return nil, errors.Wrap(err, "realtime: list presence")
	}
	slices.SortFunc(entries, func(a, b PresenceEntry) int {
		return cmp.Or(cmp.Compare(a.Subject.Type, b.Subject.Type), cmp.Compare(a.Subject.ID, b.Subject.ID))
	})
	var members []Member
	for _, e := range entries {
		if n := len(members); n > 0 && members[n-1].Subject == e.Subject {
			members[n-1].Connections++
			continue
		}
		m := Member{Subject: e.Subject, Connections: 1}
		if len(e.Info) > 0 {
			m.Info = json.RawMessage(e.Info)
		}
		members = append(members, m)
	}
	return members, nil
}

// presenceManager 用 PresenceStore 实现 centrifuge.PresenceManager。
type presenceManager struct {
	store   PresenceStore
	members *membership
}

func (m presenceManager) AddPresence(ch, clientID string, info *centrifuge.ClientInfo) error {
	ctx, cancel := context.WithTimeout(context.Background(), presenceTimeout)
	defer cancel()
	m.members.refreshed(ch, clientID)
	ref, err := identity.ParseRef(info.UserID)
	if err != nil {
		return errors.Wrap(err, "realtime: presence")
	}
	return m.store.Add(ctx, PresenceEntry{Channel: ch, Client: clientID, Subject: ref, Info: info.ChanInfo}, presenceTTL)
}

func (m presenceManager) RemovePresence(ch, clientID, _ string) error {
	return m.members.remove(ch, clientID)
}

func (m presenceManager) Presence(ch string) (map[string]*centrifuge.ClientInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), presenceTimeout)
	defer cancel()
	entries, err := m.store.List(ctx, ch)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*centrifuge.ClientInfo, len(entries))
	for _, e := range entries {
		out[e.Client] = &centrifuge.ClientInfo{ClientID: e.Client, UserID: e.Subject.String(), ChanInfo: e.Info}
	}
	return out, nil
}

func (m presenceManager) PresenceStats(ch string) (centrifuge.PresenceStats, error) {
	clients, err := m.Presence(ch)
	if err != nil {
		return centrifuge.PresenceStats{}, err
	}
	users := map[string]struct{}{}
	for _, c := range clients {
		users[c.UserID] = struct{}{}
	}
	return centrifuge.PresenceStats{NumClients: len(clients), NumUsers: len(users)}, nil
}

// noPresence 是没有设置 Options.Presence 时使用的存储：没有 presence 频道，也就没有在线记录。
type noPresence struct{}

func (noPresence) Add(context.Context, PresenceEntry, time.Duration) error {
	return errors.New("realtime: Options.Presence is not set")
}

func (noPresence) Remove(context.Context, string, string) error { return nil }

func (noPresence) List(context.Context, string) ([]PresenceEntry, error) { return nil, nil }
