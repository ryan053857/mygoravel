package realtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/centrifugal/centrifuge"

	"github.com/ryan053857/mygoravel/broadcast"
)

// internalTopic 是 realtime 在 Hub 上使用的主题：节点之间的控制消息、客户端发布的消息和 join/leave。
const internalTopic = "realtime.internal"

// publishTimeout 是 bridge 向 Hub 发布一条消息的时限。
const publishTimeout = 5 * time.Second

const (
	kindControl = "ctl"
	kindPublish = "pub"
	kindJoin    = "join"
	kindLeave   = "leave"
)

// envelope 是 internalTopic 上的消息。
type envelope struct {
	Kind    string                 `json:"k"`
	Node    string                 `json:"n,omitempty"` // 控制消息的目标节点，空表示所有节点
	Channel string                 `json:"c,omitempty"`
	Data    []byte                 `json:"d,omitempty"`
	Info    *centrifuge.ClientInfo `json:"i,omitempty"`
}

// bridge 用 broadcast.Hub 实现 centrifuge.Broker 和 centrifuge.Controller：
// 频道就是 Hub 的主题，应用通过 Hub 发布的消息直接交给订阅了同名频道的客户端。
// 每个实例本来就收到全部主题，所以按频道订阅和退订不需要做任何事。不保存历史。
type bridge struct {
	hub  *broadcast.Hub
	node *centrifuge.Node
	pubs centrifuge.BrokerEventHandler
	ctl  centrifuge.ControlEventHandler
}

func (b *bridge) RegisterBrokerEventHandler(h centrifuge.BrokerEventHandler) error {
	b.pubs = h
	return nil
}

func (b *bridge) RegisterControlEventHandler(h centrifuge.ControlEventHandler) error {
	b.ctl = h
	return nil
}

func (b *bridge) Subscribe(...string) error   { return nil }
func (b *bridge) Unsubscribe(...string) error { return nil }

func (b *bridge) Publish(ch string, data []byte, opts centrifuge.PublishOptions) (centrifuge.PublishResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if opts.ClientInfo == nil {
		return centrifuge.PublishResult{}, b.hub.Publish(ctx, ch, data)
	}
	// 客户端发布的消息带有发布者的信息，只发给 WebSocket 客户端。
	return centrifuge.PublishResult{}, b.send(ctx, envelope{Kind: kindPublish, Channel: ch, Data: data, Info: opts.ClientInfo})
}

func (b *bridge) PublishJoin(ch string, info *centrifuge.ClientInfo) error {
	return b.sendNow(envelope{Kind: kindJoin, Channel: ch, Info: info})
}

func (b *bridge) PublishLeave(ch string, info *centrifuge.ClientInfo) error {
	return b.sendNow(envelope{Kind: kindLeave, Channel: ch, Info: info})
}

func (b *bridge) PublishControl(data []byte, nodeID, _ string) error {
	return b.sendNow(envelope{Kind: kindControl, Node: nodeID, Data: data})
}

func (b *bridge) History(string, centrifuge.HistoryOptions) ([]*centrifuge.Publication, centrifuge.StreamPosition, error) {
	return nil, centrifuge.StreamPosition{}, centrifuge.ErrorNotAvailable
}

func (b *bridge) RemoveHistory(string) error { return nil }

func (b *bridge) sendNow(e envelope) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	return b.send(ctx, e)
}

func (b *bridge) send(ctx context.Context, e envelope) error {
	return b.hub.PublishJSON(ctx, internalTopic, e)
}

// deliver 处理本实例从 Hub 收到的每一条消息。它在 Hub 的接收循环中调用，不能阻塞。
func (b *bridge) deliver(msg broadcast.Message) {
	if msg.Topic != internalTopic {
		// 没有客户端订阅的主题直接跳过。JSON 协议的客户端只能接收 JSON，其他内容（例如空消息）不转发。
		if b.node.Hub().NumSubscribers(msg.Topic) == 0 || !json.Valid(msg.Data) {
			return
		}
		_ = b.pubs.HandlePublication(msg.Topic, &centrifuge.Publication{Data: msg.Data}, centrifuge.StreamPosition{}, false, nil)
		return
	}
	var e envelope
	if json.Unmarshal(msg.Data, &e) != nil {
		return
	}
	switch e.Kind {
	case kindControl:
		if e.Node == "" || e.Node == b.node.ID() {
			_ = b.ctl.HandleControl(e.Data)
		}
	case kindPublish:
		_ = b.pubs.HandlePublication(e.Channel, &centrifuge.Publication{Data: e.Data, Info: e.Info}, centrifuge.StreamPosition{}, false, nil)
	case kindJoin:
		_ = b.pubs.HandleJoin(e.Channel, e.Info)
	case kindLeave:
		_ = b.pubs.HandleLeave(e.Channel, e.Info)
	}
}

// lost 在 Hub 的后端可能丢了消息时调用：断开本实例的全部客户端，SDK 会自动重连，
// 应用在重新订阅成功后取回期间的数据。
func (b *bridge) lost(error) {
	for _, c := range b.node.Hub().Connections() {
		c.Disconnect(centrifuge.DisconnectInsufficientState)
	}
}
