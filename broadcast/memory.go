package broadcast

import "context"

// memory 是只在本进程内分发的 Backend，Hub 直接投递，不经过 Listen。
type memory struct{}

// Memory 返回只在本进程内分发消息的 Backend，适合单实例部署（包括 SQLite）、开发和测试。
// 每个 Hub 各自分发，不同 Hub 之间不互通。
func Memory() Backend { return memory{} }

func (memory) Publish(context.Context, Message) error { return nil }

func (memory) Listen(ctx context.Context, _ func(Message), _ func(error)) { <-ctx.Done() }
