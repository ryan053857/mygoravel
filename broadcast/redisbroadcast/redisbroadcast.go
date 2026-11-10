// Package redisbroadcast 是基于 go-redis Pub/Sub 的 broadcast.Backend，多个实例共享同一个频道。
//
//	hub := broadcast.New(redisbroadcast.New(rdb, "blog:broadcast"), nil)
package redisbroadcast

import (
	"context"
	"errors"
	"net"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ryan053857/mygoravel/broadcast"
)

var _ broadcast.Backend = (*Backend)(nil)

// pingInterval 是订阅连接空闲时发送 PING 的间隔；再过一个间隔仍没有任何回应，就认为连接已断开。
var pingInterval = 15 * time.Second

// Backend 把所有主题的消息发布到同一个 Redis 频道，每个实例订阅这个频道后在本地按主题分发。
// Redis Pub/Sub 不保存消息：订阅连接中断期间发布的消息会丢失，Backend 因此报告丢失，
// Hub 随之以 broadcast.ErrLost 结束全部订阅，客户端重连后由应用补发。
type Backend struct {
	rdb     redis.UniversalClient
	channel string
}

// New 创建 Redis Backend。channel 是使用的 Redis 频道，例如 blog:broadcast，同一个应用的实例必须相同。
// rdb 为 nil 或 channel 为空时 panic。
func New(rdb redis.UniversalClient, channel string) *Backend {
	switch {
	case rdb == nil:
		panic("redisbroadcast: nil client")
	case channel == "":
		panic("redisbroadcast: empty channel")
	}
	return &Backend{rdb: rdb, channel: channel}
}

// Publish 实现 broadcast.Backend。消息编码为“主题\n数据”，主题中不会出现换行。
func (b *Backend) Publish(ctx context.Context, msg broadcast.Message) error {
	return b.rdb.Publish(ctx, b.channel, msg.Topic+"\n"+string(msg.Data)).Err()
}

// Listen 实现 broadcast.Backend：订阅频道；连接出错或 PING 没有回应时报告丢失，并重新订阅。
func (b *Backend) Listen(ctx context.Context, deliver func(broadcast.Message), lost func(error)) {
	backoff := 100 * time.Millisecond
	for {
		ps := b.rdb.Subscribe(ctx, b.channel)
		// go-redis 阻塞读取时不理会 ctx：ctx 结束时关闭连接，让 receive 立即返回。
		stop := context.AfterFunc(ctx, func() { _ = ps.Close() })
		subscribed, err := b.receive(ctx, ps, deliver)
		stop()
		_ = ps.Close()
		if ctx.Err() != nil {
			return
		}
		lost(err)
		if subscribed {
			backoff = 100 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 5*time.Second)
	}
}

// receive 读取订阅连接上的消息，直到出错。subscribed 报告是否曾订阅成功。
func (b *Backend) receive(ctx context.Context, ps *redis.PubSub, deliver func(broadcast.Message)) (subscribed bool, err error) {
	waitingPong := false
	for {
		v, err := ps.ReceiveTimeout(ctx, pingInterval)
		if err != nil {
			var nerr net.Error
			if !errors.As(err, &nerr) || !nerr.Timeout() {
				return subscribed, err
			}
			if waitingPong {
				return subscribed, errors.New("redisbroadcast: no reply to PING")
			}
			if err := ps.Ping(ctx); err != nil {
				return subscribed, err
			}
			waitingPong = true
			continue
		}
		waitingPong = false
		switch v := v.(type) {
		case *redis.Subscription:
			subscribed = true
		case *redis.Message:
			topic, data, ok := strings.Cut(v.Payload, "\n")
			if ok {
				deliver(broadcast.Message{Topic: topic, Data: []byte(data)})
			}
		}
	}
}
