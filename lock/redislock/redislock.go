// Package redislock 是基于 go-redis 的锁存储，实现 lock.Store。
//
//	locker := lock.New(redislock.New(rdb))
package redislock

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/lock"
)

var _ lock.Store = (*Store)(nil)

// Store 把每个锁保存为一个 Redis 键：键名就是锁名，值是持有者，键的过期时间就是锁的有效期。
// 过期由 Redis 服务端计算，不依赖各实例的时钟；过期的键由 Redis 自动删除，不需要清理。
type Store struct {
	rdb redis.UniversalClient
}

// New 创建 Redis 锁存储。单机、哨兵和集群客户端都可以使用。
func New(rdb redis.UniversalClient) *Store {
	if rdb == nil {
		panic("redislock: nil client")
	}
	return &Store{rdb: rdb}
}

// Refresh 和 Release 先比较持有者再修改，用脚本保证两步之间不会被其他持有者插入。
var (
	refresh = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0`)
	release = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
	return redis.call("DEL", KEYS[1])
end
return 0`)
)

// Acquire 实现 lock.Store，对应 SET name owner NX PX ttl。
func (s *Store) Acquire(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return s.rdb.SetNX(ctx, name, owner, ttl).Result()
}

// Refresh 实现 lock.Store。
func (s *Store) Refresh(ctx context.Context, name, owner string, ttl time.Duration) (bool, error) {
	return s.run(ctx, refresh, name, owner, max(ttl.Milliseconds(), 1))
}

// Release 实现 lock.Store。
func (s *Store) Release(ctx context.Context, name, owner string) (bool, error) {
	return s.run(ctx, release, name, owner)
}

func (s *Store) run(ctx context.Context, script *redis.Script, name string, args ...any) (bool, error) {
	n, err := script.Run(ctx, s.rdb, []string{name}, args...).Int64()
	return n == 1, err
}
