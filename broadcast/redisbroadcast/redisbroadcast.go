// Package redisbroadcast 是基于 Redis Pub/Sub 的 broadcast.Backend（rueidis），多个实例共享同一个频道。
//
//	hub := broadcast.New(redisbroadcast.New(rdb, "blog:broadcast"), nil)
package redisbroadcast

import (
	"context"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/redis/rueidis"

	"github.com/ryan053857/mygoravel/broadcast"
)

var _ broadcast.Backend = (*Backend)(nil)

// pingInterval 是订阅连接发送 PING 的间隔；PING 在一个间隔内没有回应，就认为连接已断开。
var pingInterval = 15 * time.Second

// Backend 把所有主题的消息发布到同一个 Redis 频道，每个实例订阅这个频道后在本地按主题分发。
// Redis Pub/Sub 不保存消息：订阅连接中断期间发布的消息会丢失，Backend 因此报告丢失，
// Hub 随之以 broadcast.ErrLost 结束全部订阅，客户端重连后由应用补发。
type Backend struct {
	rdb     rueidis.Client
	channel string
}

// New 创建 Redis Backend。channel 是使用的 Redis 频道，例如 blog:broadcast，同一个应用的实例必须相同。
// rdb 为 nil 或 channel 为空时 panic。
func New(rdb rueidis.Client, channel string) *Backend {
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
	return b.rdb.Do(ctx, b.rdb.B().Publish().Channel(b.channel).Message(msg.Topic+"\n"+string(msg.Data)).Build()).Error()
}

// Listen 实现 broadcast.Backend：订阅频道；连接出错或 PING 没有回应时报告丢失，并重新订阅。
func (b *Backend) Listen(ctx context.Context, deliver func(broadcast.Message), lost func(error)) {
	backoff := 100 * time.Millisecond
	for {
		subscribed, err := b.listen(ctx, deliver)
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

// listen 在一条专用连接上订阅频道并接收消息，直到连接出错或 ctx 结束。subscribed 报告是否曾订阅成功。
func (b *Backend) listen(ctx context.Context, deliver func(broadcast.Message)) (subscribed bool, err error) {
	c, release := b.rdb.Dedicate()
	defer release()
	closed := c.SetPubSubHooks(rueidis.PubSubHooks{
		OnMessage: func(m rueidis.PubSubMessage) {
			if topic, data, ok := strings.Cut(m.Message, "\n"); ok {
				deliver(broadcast.Message{Topic: topic, Data: []byte(data)})
			}
		},
	})
	if err := c.Do(ctx, c.B().Subscribe().Channel(b.channel).Build()).Error(); err != nil {
		return false, errors.Wrap(err, "redisbroadcast: subscribe")
	}
	ping := time.NewTicker(pingInterval)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return true, ctx.Err()
		case err := <-closed:
			if err == nil {
				err = errors.New("redisbroadcast: subscription closed")
			}
			return true, err
		case <-ping.C:
			pctx, cancel := context.WithTimeout(ctx, pingInterval)
			err := c.Do(pctx, c.B().Ping().Build()).Error()
			cancel()
			if err != nil && ctx.Err() == nil {
				return true, errors.Wrap(err, "redisbroadcast: no reply to PING")
			}
		}
	}
}
