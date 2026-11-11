package realtime_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

const wait = 5 * time.Second

// client 是测试用的最小 Centrifuge JSON 协议客户端。
type client struct {
	t    *testing.T
	conn *websocket.Conn
	id   int
	in   chan frame // 后台读到的帧，连接结束时关闭
	err  error      // 连接结束的原因，in 关闭后可读
	// pending 保存已经取出、还没被使用的帧。
	pending []frame
}

type frame struct {
	ID        int             `json:"id"`
	Error     *protoError     `json:"error"`
	Connect   json.RawMessage `json:"connect"`
	Subscribe json.RawMessage `json:"subscribe"`
	Publish   json.RawMessage `json:"publish"`
	Presence  *struct {
		Presence map[string]memberInfo `json:"presence"`
	} `json:"presence"`
	Push *push `json:"push"`
}

type protoError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// memberInfo 是 presence 结果和 join/leave 事件中的一个连接。
type memberInfo struct {
	User     string          `json:"user"`
	Client   string          `json:"client"`
	ChanInfo json.RawMessage `json:"chan_info"`
}

type membership struct {
	Info memberInfo `json:"info"`
}

type push struct {
	Channel     string          `json:"channel"`
	Unsubscribe json.RawMessage `json:"unsubscribe"`
	Join        *membership     `json:"join"`
	Leave       *membership     `json:"leave"`
	Pub         *struct {
		Data json.RawMessage `json:"data"`
		Info *struct {
			User   string `json:"user"`
			Client string `json:"client"`
		} `json:"info"`
	} `json:"pub"`
}

// dial 建立 WebSocket 连接，不发送 connect 命令。
func dial(t *testing.T, url string, header http.Header) (*client, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(url, "http"), &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	c := &client{t: t, conn: conn, in: make(chan frame, 256)}
	go c.loop()
	return c, resp, nil
}

// connect 建立连接并发送 connect 命令，返回 connect 的结果。
func connect(t *testing.T, url, token string, header http.Header) (*client, frame) {
	t.Helper()
	c, _, err := dial(t, url, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	return c, c.call(map[string]any{"connect": map[string]any{"token": token, "name": "test"}})
}

// rejected 发送 connect 命令，返回服务器拒绝连接时的关闭码。
func rejected(t *testing.T, url, token string, header http.Header) websocket.StatusCode {
	t.Helper()
	c, _, err := dial(t, url, header)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	data, _ := json.Marshal(map[string]any{"id": 1, "connect": map[string]any{"token": token}})
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("write: %v", err)
	}
	return c.closed()
}

// mustConnect 要求连接成功。
func mustConnect(t *testing.T, url, token string, header http.Header) *client {
	t.Helper()
	c, reply := connect(t, url, token, header)
	if reply.Error != nil || reply.Connect == nil {
		t.Fatalf("connect: %+v", reply)
	}
	return c
}

// call 发送一条命令并等待它的回复，期间收到的推送留给 push。
func (c *client) call(cmd map[string]any) frame {
	c.t.Helper()
	c.id++
	cmd["id"] = c.id
	data, _ := json.Marshal(cmd)
	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		c.t.Fatalf("write: %v", err)
	}
	for {
		for i, f := range c.pending {
			if f.ID == c.id {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				return f
			}
		}
		if err := c.read(wait); err != nil {
			c.t.Fatalf("waiting for reply to %s: %v", data, err)
		}
	}
}

func (c *client) subscribe(channel string) frame {
	c.t.Helper()
	return c.call(map[string]any{"subscribe": map[string]any{"channel": channel}})
}

func (c *client) mustSubscribe(channel string) {
	c.t.Helper()
	if reply := c.subscribe(channel); reply.Error != nil {
		c.t.Fatalf("subscribe %s: %+v", channel, reply.Error)
	}
}

func (c *client) publish(channel string, data any) frame {
	c.t.Helper()
	return c.call(map[string]any{"publish": map[string]any{"channel": channel, "data": data}})
}

// push 返回下一条推送。
func (c *client) push() *push {
	c.t.Helper()
	p, err := c.tryPush(wait)
	if err != nil {
		c.t.Fatalf("waiting for push: %v", err)
	}
	return p
}

// unsubscribed 等待服务器取消订阅的通知，返回频道。
func (c *client) unsubscribed() string {
	c.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		for i, f := range c.pending {
			if f.Push != nil && f.Push.Unsubscribe != nil {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				return f.Push.Channel
			}
		}
		if err := c.read(time.Until(deadline)); err != nil {
			c.t.Fatalf("waiting for unsubscribe: %v", err)
		}
	}
}

// membership 等待下一个 join 或 leave 事件，返回事件类型和成员。
func (c *client) membership() (kind string, info memberInfo) {
	c.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		for i, f := range c.pending {
			if f.Push == nil || (f.Push.Join == nil && f.Push.Leave == nil) {
				continue
			}
			c.pending = append(c.pending[:i], c.pending[i+1:]...)
			if f.Push.Join != nil {
				return "join", f.Push.Join.Info
			}
			return "leave", f.Push.Leave.Info
		}
		if err := c.read(time.Until(deadline)); err != nil {
			c.t.Fatalf("waiting for join or leave: %v", err)
		}
	}
}

// noMembership 确认 d 之内没有 join 或 leave 事件。
func (c *client) noMembership(d time.Duration) {
	c.t.Helper()
	deadline := time.Now().Add(d)
	for {
		for _, f := range c.pending {
			if f.Push != nil && f.Push.Join != nil {
				c.t.Fatalf("unexpected join of %s", f.Push.Join.Info.User)
			}
			if f.Push != nil && f.Push.Leave != nil {
				c.t.Fatalf("unexpected leave of %s", f.Push.Leave.Info.User)
			}
		}
		if err := c.read(time.Until(deadline)); errors.Is(err, context.DeadlineExceeded) {
			return
		} else if err != nil {
			c.t.Fatalf("connection ended: %v", err)
		}
	}
}

// unsubscribe 主动取消订阅。
func (c *client) unsubscribe(channel string) {
	c.t.Helper()
	if reply := c.call(map[string]any{"unsubscribe": map[string]any{"channel": channel}}); reply.Error != nil {
		c.t.Fatalf("unsubscribe %s: %+v", channel, reply.Error)
	}
}

// presence 读取频道的在线成员，返回每个主体的连接数。
func (c *client) presence(channel string) (map[string]int, *protoError) {
	c.t.Helper()
	reply := c.call(map[string]any{"presence": map[string]any{"channel": channel}})
	if reply.Error != nil {
		return nil, reply.Error
	}
	users := map[string]int{}
	if reply.Presence != nil {
		for _, m := range reply.Presence.Presence {
			users[m.User]++
		}
	}
	return users, nil
}

// close 主动断开连接。
func (c *client) close() {
	_ = c.conn.Close(websocket.StatusNormalClosure, "")
}

// noPush 确认 d 之内没有推送。
func (c *client) noPush(d time.Duration) {
	c.t.Helper()
	if p, err := c.tryPush(d); err == nil {
		c.t.Fatalf("unexpected push on %s: %s", p.Channel, p.Pub.Data)
	} else if !errors.Is(err, context.DeadlineExceeded) {
		c.t.Fatalf("connection ended: %v", err)
	}
}

func (c *client) tryPush(d time.Duration) (*push, error) {
	deadline := time.Now().Add(d)
	for {
		for i, f := range c.pending {
			if f.Push != nil && f.Push.Pub != nil {
				c.pending = append(c.pending[:i], c.pending[i+1:]...)
				return f.Push, nil
			}
		}
		if err := c.read(time.Until(deadline)); err != nil {
			return nil, err
		}
	}
}

// closed 等待服务器断开连接，返回关闭码。
func (c *client) closed() websocket.StatusCode {
	c.t.Helper()
	deadline := time.Now().Add(wait)
	for {
		err := c.read(time.Until(deadline))
		if err == nil {
			continue
		}
		if code := websocket.CloseStatus(err); code != -1 {
			return code
		}
		c.t.Fatalf("waiting for close: %v", err)
	}
}

// read 等待后台读到的下一帧，放入 pending。连接结束时返回读取的错误。
func (c *client) read(d time.Duration) error {
	if d <= 0 {
		return context.DeadlineExceeded
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case f, ok := <-c.in:
		if !ok {
			return c.err
		}
		c.pending = append(c.pending, f)
		return nil
	case <-timer.C:
		return context.DeadlineExceeded
	}
}

// loop 在后台读取连接：coder/websocket 在读取的 context 结束时会关闭连接，所以读取不带超时。
func (c *client) loop() {
	defer close(c.in)
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.err = err
			return
		}
		// 一个 WebSocket 消息中可能有多个以换行分隔的帧；{} 是服务器的 ping。
		for _, line := range strings.Split(string(data), "\n") {
			var f frame
			if json.Unmarshal([]byte(line), &f) == nil {
				c.in <- f
			}
		}
	}
}
