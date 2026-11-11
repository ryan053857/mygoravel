package broadcast

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"sync"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging"
)

// bufferSize 是每个订阅的缓冲区大小，满了就以 ErrSlow 结束订阅。
const bufferSize = 64

// Hub 是进程内的广播中心，可以并发使用。每个进程创建一个，所有长连接共用。
type Hub struct {
	backend Backend
	logger  *slog.Logger

	mu     sync.Mutex
	topics map[string]map[*Subscription]struct{}
	taps   []*tap // 写时复制，读取后可以在锁外使用
	closed bool

	cancel context.CancelFunc
	done   chan struct{}
}

// New 创建 Hub，并在后台开始从 backend 接收消息，直到 Close。logger 记录 Backend 丢失消息的情况，
// nil 时使用 slog.Default()。backend 为 nil 时 panic。
func New(backend Backend, logger *slog.Logger) *Hub {
	if backend == nil {
		panic("broadcast: nil backend")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h := &Hub{
		backend: backend,
		logger:  logging.Or(logger).With(logging.AttrComponent, "broadcast"),
		topics:  map[string]map[*Subscription]struct{}{},
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go func() {
		defer close(h.done)
		backend.Listen(ctx, h.deliver, h.lost)
	}()
	return h
}

// Publish 把 data 作为 topic 主题的消息发给所有实例的订阅。topic 不合法时返回错误，见 ValidateTopic。
// 它不参与数据库事务：需要在业务数据提交之后才通知时，在提交之后调用。
func (h *Hub) Publish(ctx context.Context, topic string, data []byte) error {
	if err := ValidateTopic(topic); err != nil {
		return err
	}
	return errors.Wrap(h.backend.Publish(ctx, Message{Topic: topic, Data: data}), "broadcast: publish")
}

// PublishJSON 把 v 编码为 JSON 后发布。
func (h *Hub) PublishJSON(ctx context.Context, topic string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return errors.Wrap(err, "broadcast: encode")
	}
	return h.Publish(ctx, topic, data)
}

// Subscribe 订阅 topics 中的全部主题。主题不合法时 panic。Hub 已关闭时返回一个已结束的订阅。
func (h *Hub) Subscribe(topics ...string) *Subscription {
	if len(topics) == 0 {
		panic("broadcast: Subscribe needs at least one topic")
	}
	for _, topic := range topics {
		if err := ValidateTopic(topic); err != nil {
			panic(err)
		}
	}
	s := &Subscription{hub: h, topics: topics, ch: make(chan Message, bufferSize)}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		s.end(ErrClosed)
		return s
	}
	for _, topic := range topics {
		subs := h.topics[topic]
		if subs == nil {
			subs = map[*Subscription]struct{}{}
			h.topics[topic] = subs
		}
		subs[s] = struct{}{}
	}
	return s
}

// tap 是 Listen 登记的一对回调。
type tap struct {
	deliver func(Message)
	lost    func(error)
}

// Listen 登记 deliver 接收本实例收到的每一条消息，不区分主题；Backend 可能丢了消息时调用 lost。
// 它供需要自己按主题分发的组件使用，例如 realtime 把消息转给 WebSocket 客户端；业务代码用 Subscribe。
//
// 两个回调按消息到达的顺序依次调用，不能阻塞：慢的回调会拖住本实例的全部订阅。
// 回调中不能直接调用 Publish：有的后端在 Publish 返回之前投递给本进程，回调又在投递中，会互相等待；
// 需要发布时交给另一个 goroutine。lost 可以为 nil。
// 返回的函数取消登记，可以重复调用。
func (h *Hub) Listen(deliver func(Message), lost func(error)) (stop func()) {
	if deliver == nil {
		panic("broadcast: nil deliver")
	}
	t := &tap{deliver: deliver, lost: lost}
	h.mu.Lock()
	h.taps = append(h.taps[:len(h.taps):len(h.taps)], t)
	h.mu.Unlock()
	return func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if i := slices.Index(h.taps, t); i >= 0 {
			h.taps = slices.Delete(slices.Clone(h.taps), i, i+1)
		}
	}
}

// Close 停止接收消息，并以 ErrClosed 结束全部订阅。之后的 Publish 仍会交给 Backend。
func (h *Hub) Close() error {
	h.cancel()
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	h.endAll(ErrClosed)
	return nil
}

func (h *Hub) deliver(msg Message) {
	h.mu.Lock()
	for s := range h.topics[msg.Topic] {
		select {
		case s.ch <- msg:
		default:
			h.remove(s)
			s.end(ErrSlow)
		}
	}
	taps := h.taps
	h.mu.Unlock()
	// 在锁外调用，回调可以订阅或取消登记。
	for _, t := range taps {
		t.deliver(msg)
	}
}

func (h *Hub) lost(err error) {
	h.logger.Warn("broadcast messages may have been lost; ending subscriptions", "error", err)
	h.mu.Lock()
	h.endAll(errors.WithSecondaryError(ErrLost, err))
	taps := h.taps
	h.mu.Unlock()
	for _, t := range taps {
		if t.lost != nil {
			t.lost(err)
		}
	}
}

// endAll 以 err 结束全部订阅。调用方持有 h.mu。
func (h *Hub) endAll(err error) {
	for topic, subs := range h.topics {
		for s := range subs {
			s.end(err)
		}
		delete(h.topics, topic)
	}
}

// remove 把 s 从全部主题中移除。调用方持有 h.mu。
func (h *Hub) remove(s *Subscription) {
	for _, topic := range s.topics {
		delete(h.topics[topic], s)
		if len(h.topics[topic]) == 0 {
			delete(h.topics, topic)
		}
	}
}

// Subscription 是一个订阅。消息从 C 读取；订阅结束时 C 被关闭，Err 返回原因。
type Subscription struct {
	hub    *Hub
	topics []string
	ch     chan Message
	err    error // 由 hub.mu 保护
}

// C 返回接收消息的通道。订阅结束时它被关闭，缓冲区中尚未读取的消息仍可读出。
func (s *Subscription) C() <-chan Message { return s.ch }

// Err 返回订阅结束的原因：ErrClosed、ErrSlow 或 ErrLost；订阅仍在进行时为 nil。
func (s *Subscription) Err() error {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	return s.err
}

// Close 结束订阅。可以重复调用。
func (s *Subscription) Close() {
	s.hub.mu.Lock()
	defer s.hub.mu.Unlock()
	if s.err == nil {
		s.hub.remove(s)
		s.end(ErrClosed)
	}
}

// end 记录原因并关闭通道。调用方持有 hub.mu。
func (s *Subscription) end(err error) {
	if s.err != nil {
		return
	}
	s.err = err
	close(s.ch)
}
