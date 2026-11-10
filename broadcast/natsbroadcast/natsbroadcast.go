// Package natsbroadcast 是基于 NATS 核心发布订阅的 broadcast.Backend，不需要 JetStream。
//
//	hub := broadcast.New(natsbroadcast.New(nc, "blog.broadcast"), nil)
package natsbroadcast

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/ryan053857/mygoravel/broadcast"
)

var _ broadcast.Backend = (*Backend)(nil)

// checkInterval 是检查连接是否重连过、订阅是否丢过消息的间隔。
var checkInterval = time.Second

// Backend 把主题 t 的消息发布到 NATS 主题 prefix.t，每个实例订阅 prefix.> 后在本地按主题分发。
// NATS 核心发布订阅不保存消息：连接重连期间发布的消息，以及订阅积压超过 NATS 客户端上限时
// 被丢弃的消息都会丢失，Backend 因此报告丢失，Hub 随之以 broadcast.ErrLost 结束全部订阅。
// 连接由应用创建和关闭，重连由 nats.go 完成。
type Backend struct {
	nc     *nats.Conn
	prefix string
}

// New 创建 NATS Backend。prefix 是使用的 NATS 主题前缀，例如 blog.broadcast，同一个应用的实例必须相同；
// 它与广播主题的规则相同，见 broadcast.ValidateTopic。nc 为 nil 或 prefix 不合法时 panic。
func New(nc *nats.Conn, prefix string) *Backend {
	if nc == nil {
		panic("natsbroadcast: nil connection")
	}
	if err := broadcast.ValidateTopic(prefix); err != nil {
		panic("natsbroadcast: prefix: " + err.Error())
	}
	return &Backend{nc: nc, prefix: prefix + "."}
}

// Publish 实现 broadcast.Backend。
func (b *Backend) Publish(_ context.Context, msg broadcast.Message) error {
	return b.nc.Publish(b.prefix+msg.Topic, msg.Data)
}

// Listen 实现 broadcast.Backend：订阅 prefix.>；连接重连过或订阅丢过消息时报告丢失，并重新订阅。
func (b *Backend) Listen(ctx context.Context, deliver func(broadcast.Message), lost func(error)) {
	for {
		sub, err := b.nc.Subscribe(b.prefix+">", func(m *nats.Msg) {
			deliver(broadcast.Message{Topic: strings.TrimPrefix(m.Subject, b.prefix), Data: m.Data})
		})
		if err == nil {
			err = b.nc.Flush() // 等服务器登记订阅
		}
		if err == nil {
			err = b.watch(ctx, sub)
		}
		if sub != nil {
			_ = sub.Unsubscribe()
		}
		if ctx.Err() != nil {
			return
		}
		lost(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(checkInterval):
		}
	}
}

// watch 定期检查连接和订阅，发现可能丢失消息时返回原因；ctx 结束时返回 nil。
func (b *Backend) watch(ctx context.Context, sub *nats.Subscription) error {
	reconnects := b.nc.Stats().Reconnects
	t := time.NewTicker(checkInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		switch dropped, _ := sub.Dropped(); {
		case b.nc.Stats().Reconnects != reconnects:
			return errors.New("natsbroadcast: connection reconnected")
		case dropped > 0:
			return errors.New("natsbroadcast: subscription dropped messages")
		case !sub.IsValid():
			return errors.New("natsbroadcast: subscription closed")
		}
	}
}
