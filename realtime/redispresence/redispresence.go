// Package redispresence 是基于 go-redis 的在线成员存储，实现 realtime.PresenceStore。
//
//	rt, err := realtime.New(hub, realtime.Options{Presence: redispresence.New(rdb, "blog:presence:")})
package redispresence

import (
	"context"
	"encoding/json"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/realtime"
)

var _ realtime.PresenceStore = (*Store)(nil)

// Store 为每个频道保存两个键：有序集合 <prefix>{<channel>}:exp 记录每个连接的过期时间，
// 哈希 <prefix>{<channel>}:data 记录每个连接的主体和资料。两个键带有过期时间，没有成员的频道由 Redis 自动删除；
// 频道名放在花括号中，集群中两个键落在同一个槽。
// 过期时间按应用服务器时钟计算，各实例的时钟需要同步。
type Store struct {
	rdb    redis.UniversalClient
	prefix string
}

// New 创建 Redis 在线成员存储。prefix 区分不同的应用，同一个应用的实例必须相同。
// 单机、哨兵和集群客户端都可以使用。
func New(rdb redis.UniversalClient, prefix string) *Store {
	if rdb == nil {
		panic("redispresence: nil client")
	}
	return &Store{rdb: rdb, prefix: prefix}
}

type stored struct {
	Subject string `json:"s"`
	Info    []byte `json:"i,omitempty"`
}

func (s *Store) keys(channel string) (exp, data string) {
	return s.prefix + "{" + channel + "}:exp", s.prefix + "{" + channel + "}:data"
}

// Add 实现 realtime.PresenceStore。
func (s *Store) Add(ctx context.Context, e realtime.PresenceEntry, ttl time.Duration) error {
	value, err := json.Marshal(stored{Subject: e.Subject, Info: e.Info})
	if err != nil {
		return errors.Wrap(err, "redispresence: encode")
	}
	exp, data := s.keys(e.Channel)
	_, err = s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZAdd(ctx, exp, redis.Z{Score: float64(time.Now().Add(ttl).UnixMilli()), Member: e.Client})
		p.HSet(ctx, data, e.Client, value)
		p.PExpire(ctx, exp, ttl)
		p.PExpire(ctx, data, ttl)
		return nil
	})
	return errors.Wrap(err, "redispresence: add")
}

// Remove 实现 realtime.PresenceStore。
func (s *Store) Remove(ctx context.Context, channel, client string) error {
	exp, data := s.keys(channel)
	_, err := s.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZRem(ctx, exp, client)
		p.HDel(ctx, data, client)
		return nil
	})
	return errors.Wrap(err, "redispresence: remove")
}

// list 先删除已经过期的连接（每次最多 1000 个），再返回其余连接和它们的数据，整个过程是原子的。
// 续期过的连接分数已经大于当前时间，不会被误删。返回值是连接 ID 和数据交替排列的数组。
var list = redis.NewScript(`
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

// List 实现 realtime.PresenceStore，并顺便删除已经过期的连接。
func (s *Store) List(ctx context.Context, channel string) ([]realtime.PresenceEntry, error) {
	exp, data := s.keys(channel)
	pairs, err := list.Run(ctx, s.rdb, []string{exp, data}, time.Now().UnixMilli()).StringSlice()
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, errors.Wrap(err, "redispresence: list")
	}
	out := make([]realtime.PresenceEntry, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		var st stored
		if json.Unmarshal([]byte(pairs[i+1]), &st) != nil {
			continue
		}
		out = append(out, realtime.PresenceEntry{Channel: channel, Client: pairs[i], Subject: st.Subject, Info: st.Info})
	}
	return out, nil
}
