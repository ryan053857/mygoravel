// Package redispresence 是基于 rueidis 的在线成员存储，实现 realtime.PresenceStore。
//
//	rt, err := realtime.New(hub, realtime.Options{Presence: redispresence.New(rdb, "blog:presence:")})
package redispresence

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/redis/rueidis"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/realtime"
)

var _ realtime.PresenceStore = (*Store)(nil)

// Store 为每个频道保存两个键：有序集合 <prefix>{<channel>}:exp 记录每个连接的过期时间，
// 哈希 <prefix>{<channel>}:data 记录每个连接的主体和资料。两个键带有过期时间，没有成员的频道由 Redis 自动删除；
// 频道名放在花括号中，集群中两个键落在同一个槽。
// 过期时间按应用服务器时钟计算，各实例的时钟需要同步。
type Store struct {
	rdb    rueidis.Client
	prefix string
}

// New 创建 Redis 在线成员存储。prefix 区分不同的应用，同一个应用的实例必须相同。
// 单机、哨兵和集群客户端都可以使用。
func New(rdb rueidis.Client, prefix string) *Store {
	if rdb == nil {
		panic("redispresence: nil client")
	}
	return &Store{rdb: rdb, prefix: prefix}
}

type stored struct {
	Subject string `json:"s"`
	Info    []byte `json:"i,omitempty"`
}

func (s *Store) keys(channel string) []string {
	return []string{s.prefix + "{" + channel + "}:exp", s.prefix + "{" + channel + "}:data"}
}

var (
	// add 写入连接的过期时间和数据，并把两个键的过期时间延长到 ttl。
	add = rueidis.NewLuaScript(`
redis.call("ZADD", KEYS[1], ARGV[1], ARGV[2])
redis.call("HSET", KEYS[2], ARGV[2], ARGV[3])
redis.call("PEXPIRE", KEYS[1], ARGV[4])
redis.call("PEXPIRE", KEYS[2], ARGV[4])
return 0`)
	remove = rueidis.NewLuaScript(`
redis.call("ZREM", KEYS[1], ARGV[1])
redis.call("HDEL", KEYS[2], ARGV[1])
return 0`)
	// list 先删除已经过期的连接（每次最多 1000 个），再返回其余连接和它们的数据，整个过程是原子的。
	// 续期过的连接分数已经大于当前时间，不会被误删。返回值是连接 ID 和数据交替排列的数组。
	list = rueidis.NewLuaScript(`
local expired = redis.call("ZRANGEBYSCORE", KEYS[1], "-inf", ARGV[1], "LIMIT", 0, 1000)
if #expired > 0 then
	redis.call("ZREM", KEYS[1], unpack(expired))
	redis.call("HDEL", KEYS[2], unpack(expired))
end
local clients = redis.call("ZRANGEBYSCORE", KEYS[1], "(" .. ARGV[1], "+inf")
local out = {}
for _, client in ipairs(clients) do
	local value = redis.call("HGET", KEYS[2], client)
	if value then
		out[#out + 1] = client
		out[#out + 1] = value
	end
end
return out`)
)

// Add 实现 realtime.PresenceStore。
func (s *Store) Add(ctx context.Context, e realtime.PresenceEntry, ttl time.Duration) error {
	value, err := json.Marshal(stored{Subject: e.Subject.String(), Info: e.Info})
	if err != nil {
		return errors.Wrap(err, "redispresence: encode")
	}
	expires := strconv.FormatInt(time.Now().Add(ttl).UnixMilli(), 10)
	ms := strconv.FormatInt(max(ttl.Milliseconds(), 1), 10)
	err = add.Exec(ctx, s.rdb, s.keys(e.Channel), []string{expires, e.Client, string(value), ms}).Error()
	return errors.Wrap(err, "redispresence: add")
}

// Remove 实现 realtime.PresenceStore。
func (s *Store) Remove(ctx context.Context, channel, client string) error {
	return errors.Wrap(remove.Exec(ctx, s.rdb, s.keys(channel), []string{client}).Error(), "redispresence: remove")
}

// List 实现 realtime.PresenceStore，并顺便删除已经过期的连接。
func (s *Store) List(ctx context.Context, channel string) ([]realtime.PresenceEntry, error) {
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	pairs, err := list.Exec(ctx, s.rdb, s.keys(channel), []string{now}).AsStrSlice()
	if err != nil && !rueidis.IsRedisNil(err) {
		return nil, errors.Wrap(err, "redispresence: list")
	}
	out := make([]realtime.PresenceEntry, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		var st stored
		if json.Unmarshal([]byte(pairs[i+1]), &st) != nil {
			continue
		}
		ref, err := identity.ParseRef(st.Subject)
		if err != nil {
			continue
		}
		out = append(out, realtime.PresenceEntry{Channel: channel, Client: pairs[i], Subject: ref, Info: st.Info})
	}
	return out, nil
}
