package broadcasttest

import (
	"context"
	"sync"

	"github.com/ryan053857/mygoravel/broadcast"
)

// Backend 是测试用的内存 broadcast.Backend：同一个 Backend 上的多个 Hub 互相收到消息，相当于多个实例。
//
// Hub 开始监听时先收到 Backend 创建以来发布的全部消息，所以刚创建的 Hub 不会错过紧接着发布的消息。
// 之后 Publish 在返回之前把消息交给所有 Hub。Backend 保留全部消息，只用于测试；
// 生产环境中，不停机升级和多实例部署时同时运行的进程需要共用 broadcast.NewDatabase 等后端。
type Backend struct {
	mu        sync.Mutex
	published []broadcast.Message
	listeners map[*func(broadcast.Message)]struct{}
}

var _ broadcast.Backend = (*Backend)(nil)

// NewBackend 创建内存 Backend。
func NewBackend() *Backend {
	return &Backend{listeners: map[*func(broadcast.Message)]struct{}{}}
}

// Publish 实现 broadcast.Backend。
func (b *Backend) Publish(_ context.Context, msg broadcast.Message) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.published = append(b.published, msg)
	for deliver := range b.listeners {
		(*deliver)(msg)
	}
	return nil
}

// Listen 实现 broadcast.Backend。
func (b *Backend) Listen(ctx context.Context, deliver func(broadcast.Message), _ func(error)) {
	b.mu.Lock()
	for _, msg := range b.published {
		deliver(msg)
	}
	b.listeners[&deliver] = struct{}{}
	b.mu.Unlock()

	<-ctx.Done()
	b.mu.Lock()
	delete(b.listeners, &deliver)
	b.mu.Unlock()
}
