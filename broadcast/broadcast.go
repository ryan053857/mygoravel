package broadcast

import (
	"context"
	"strings"

	"github.com/cockroachdb/errors"
)

// Message 是一条广播消息。Data 通常是一个小的 JSON 领域事件，由每个订阅者自己渲染。
type Message struct {
	Topic string
	Data  []byte
}

// Backend 在实例之间传递消息。实现必须可以并发使用。
type Backend interface {
	// Publish 把消息发给所有实例，包括发布者所在的实例。
	Publish(ctx context.Context, msg Message) error
	// Listen 把所有实例发布的消息按发布顺序交给 deliver，直到 ctx 结束后返回。
	// 实现自行处理重连；可能丢失了消息时（例如与服务器的连接中断）调用 lost，然后继续接收。
	// deliver 和 lost 不会阻塞，可以在任意 goroutine 中调用。
	Listen(ctx context.Context, deliver func(Message), lost func(error))
}

var (
	// ErrClosed 表示订阅被 Close 或 Hub.Close 结束。
	ErrClosed = errors.New("broadcast: subscription closed")
	// ErrSlow 表示订阅的缓冲区已满：订阅者处理消息太慢，继续投递就要丢消息。
	ErrSlow = errors.New("broadcast: subscriber too slow")
	// ErrLost 表示 Backend 可能丢失了消息，例如与 Redis 的连接中断过。
	ErrLost = errors.New("broadcast: messages may have been lost")
)

// MaxTopicLength 是主题的最大字节数。
const MaxTopicLength = 200

// ValidateTopic 检查主题：非空，最长 MaxTopicLength 字节，只含字母、数字和 _ - : .，
// 且 . 不能在开头、结尾或连续出现。主题同时是实时频道的名称，这些规则让它可以直接出现在 URL 和日志中。
func ValidateTopic(topic string) error {
	if topic == "" || len(topic) > MaxTopicLength {
		return errors.Newf("broadcast: topic must be 1 to %d bytes", MaxTopicLength)
	}
	for _, c := range topic {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-', c == ':', c == '.':
		default:
			return errors.Newf("broadcast: topic %q contains %q", topic, c)
		}
	}
	if strings.HasPrefix(topic, ".") || strings.HasSuffix(topic, ".") || strings.Contains(topic, "..") {
		return errors.Newf("broadcast: topic %q has an empty segment", topic)
	}
	return nil
}
