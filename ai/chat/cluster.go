package chat

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/lock"
)

// 多实例协作的时间参数，测试中可以调小。
var (
	lockTTL        = 30 * time.Second      // 运行所持有的对话锁的过期时间
	lockRefresh    = 10 * time.Second      // 续期间隔
	flushInterval  = 50 * time.Millisecond // 事件批量广播的间隔
	pingInterval   = 5 * time.Second       // 没有事件时的心跳间隔
	idleTimeout    = 15 * time.Second      // 镜像多久收不到任何消息就认为运行所在的实例已经失联
	replyTimeout   = 3 * time.Second       // 等待快照和插话回执的时间
	chunkBytes     = 256 << 10             // 一条广播消息中事件数据的大致上限
	publishTimeout = 5 * time.Second       // 发布一条广播的时限
)

// Cluster 让多个实例上的 Runs 协作：任何实例都可以查看、续传、插话和取消其他实例上的运行，
// 部署多个实例时不再需要按对话做粘性路由。
//
// 对话正在运行时，运行所在的实例持有 locker 中这个对话的锁，其他实例据此判断对话是否忙；
// 运行的事件经由 hub 广播，其他实例的 Runs.Get 返回它的镜像。hub 和 locker 由应用创建和关闭，
// 所有实例必须使用同一个 Redis：redisbroadcast 和 redislock。
func Cluster(hub *broadcast.Hub, locker *lock.Locker) Option {
	return func(rs *Runs) {
		rs.cluster = &cluster{hub: hub, locker: locker, rs: rs, ping: pingInterval, idle: idleTimeout}
	}
}

type cluster struct {
	hub    *broadcast.Hub
	locker *lock.Locker
	rs     *Runs
	ping   time.Duration // 创建时取自 pingInterval，后台 goroutine 只读这份
	idle   time.Duration // 创建时取自 idleTimeout
}

// id 是对话在锁名和广播主题中的标识。主题只能包含有限的字符，所以使用哈希。
func (c *cluster) id(key session.Key) string {
	h := sha256.Sum256([]byte(key.AppName + "\x00" + key.UserID + "\x00" + key.SessionID))
	return hex.EncodeToString(h[:16])
}

// appID 是应用在广播主题中的标识，同一个广播后端上的不同应用互不干扰。
func (c *cluster) appID() string {
	h := sha256.Sum256([]byte(c.rs.app))
	return hex.EncodeToString(h[:8])
}

func (c *cluster) lockName(key session.Key) string { return "ai:run:" + c.id(key) }
func (c *cluster) runTopic(key session.Key) string { return "ai.run." + c.id(key) }
func (c *cluster) ctlTopic(key session.Key) string { return "ai.ctl." + c.id(key) }

func (c *cluster) publish(topic string, v any) error {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	return c.hub.PublishJSON(ctx, topic, v)
}

// runMsg 是运行所在的实例在运行主题上广播的消息。
type runMsg struct {
	Kind   string      `json:"kind"` // events、snapshot、ack 或 ping
	Run    string      `json:"run"`
	From   int         `json:"from,omitempty"` // events：第一个事件的序号
	Next   int         `json:"next,omitempty"` // snapshot：快照之后下一个事件的序号
	Events []wireEvent `json:"events,omitempty"`
	Req    string      `json:"req,omitempty"`
	Last   bool        `json:"last,omitempty"`  // snapshot 的最后一块
	Error  string      `json:"error,omitempty"` // ack：插话失败的原因
}

// ctlMsg 是其他实例在控制主题上发给运行所在实例的请求。
type ctlMsg struct {
	Kind    string         `json:"kind"` // snapshot、steer、notify 或 cancel
	Req     string         `json:"req,omitempty"`
	Message *model.Message `json:"message,omitempty"`
}

type wireEvent struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

func encodeEvents(evs []Event) ([]wireEvent, error) {
	out := make([]wireEvent, len(evs))
	for i, ev := range evs {
		data, err := json.Marshal(ev.Data)
		if err != nil {
			return nil, err
		}
		out[i] = wireEvent{ID: ev.ID, Name: ev.Name, Data: data}
	}
	return out, nil
}

func decodeEvent(w wireEvent) (Event, error) {
	ev := Event{ID: w.ID, Name: w.Name}
	var err error
	switch w.Name {
	case EventText, EventReasoning, EventUser:
		var d Text
		err = json.Unmarshal(w.Data, &d)
		ev.Data = d
	case EventToolCall:
		var d ToolCall
		err = json.Unmarshal(w.Data, &d)
		ev.Data = d
	case EventToolResult:
		var d ToolResult
		err = json.Unmarshal(w.Data, &d)
		ev.Data = d
	case EventError:
		var d Error
		err = json.Unmarshal(w.Data, &d)
		ev.Data = d
	case EventDone:
		var d Done
		err = json.Unmarshal(w.Data, &d)
		ev.Data = d
	default:
		err = errors.Newf("chat: unknown event %q", w.Name)
	}
	return ev, err
}

// chunks 把事件分成数据量不超过 chunkBytes 的几组，每组至少一个事件。
func chunks(evs []wireEvent) [][]wireEvent {
	var out [][]wireEvent
	var cur []wireEvent
	size := 0
	for _, ev := range evs {
		n := len(ev.Data) + len(ev.ID) + 32
		if len(cur) > 0 && size+n > chunkBytes {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, ev)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// compact 合并连续的同类文本增量，快照因此和回答的长度相当，而不是和增量的个数相当。
func compact(evs []Event) []Event {
	var out []Event
	for _, ev := range evs {
		if t, ok := ev.Data.(Text); ok && ev.Name != EventUser && len(out) > 0 {
			last := &out[len(out)-1]
			if prev, ok := last.Data.(Text); ok && last.Name == ev.Name {
				last.Data = Text{prev.Text + t.Text}
				last.ID = ev.ID
				continue
			}
		}
		out = append(out, Event{ID: ev.ID, Name: ev.Name, Data: ev.Data})
	}
	return out
}

// ---------- 运行所在的实例 ----------

// owner 是本实例上一次运行在集群中的一面：持有对话锁，广播事件，回应其他实例的请求。
type owner struct {
	c   *cluster
	key session.Key
	lk  *lock.Lock
	run *Run

	sent    int // 已经广播的事件个数
	stop    chan struct{}
	stopped chan struct{}

	closeOnce sync.Once
	closing   chan struct{}
	ctlDone   chan struct{}
}

func (c *cluster) acquire(ctx context.Context, key session.Key) (*owner, error) {
	lk, err := c.locker.Acquire(ctx, c.lockName(key), lockTTL)
	if errors.Is(err, lock.ErrNotAcquired) {
		return nil, ErrBusy.New()
	}
	if err != nil {
		return nil, err
	}
	return &owner{c: c, key: key, lk: lk, stop: make(chan struct{}), stopped: make(chan struct{}),
		closing: make(chan struct{}), ctlDone: make(chan struct{})}, nil
}

func (o *owner) release() {
	ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
	defer cancel()
	if err := o.lk.Release(ctx); err != nil && !errors.Is(err, lock.ErrNotHeld) {
		o.c.rs.logger.Warn("release conversation lock", "error", err)
	}
}

// serve 开始广播 r 的事件、续期锁，并回应控制主题上的请求。
func (o *owner) serve(r *Run) {
	o.run = r
	r.own = o
	sub := o.c.hub.Subscribe(o.c.ctlTopic(o.key))
	go func() {
		<-o.closing // close 之后不再回应控制请求
		sub.Close()
	}()
	go o.publishLoop()
	go o.control(sub)
}

func (o *owner) publishLoop() {
	defer close(o.stopped)
	flush := time.NewTicker(flushInterval)
	defer flush.Stop()
	ping := time.NewTicker(o.c.ping)
	defer ping.Stop()
	refresh := time.NewTicker(lockRefresh)
	defer refresh.Stop()
	for {
		select {
		case <-flush.C:
			o.flush()
		case <-ping.C:
			o.send(runMsg{Kind: "ping", Run: o.run.ID})
		case <-refresh.C:
			ctx, cancel := context.WithTimeout(context.Background(), publishTimeout)
			err := o.lk.Refresh(ctx, lockTTL)
			cancel()
			if errors.Is(err, lock.ErrNotHeld) {
				// 锁已经过期，其他实例可能开始了新的运行：停止这一次，避免两次运行同时写入对话。
				o.c.rs.logger.Error("conversation lock lost; canceling run", "session_id", o.key.SessionID)
				o.run.Cancel()
			} else if err != nil {
				o.c.rs.logger.Warn("refresh conversation lock", "error", err)
			}
		case <-o.stop:
			o.flush()
			return
		}
	}
}

// flush 广播还没有广播的事件。
func (o *owner) flush() {
	o.run.mu.Lock()
	evs := append([]Event(nil), o.run.events[o.sent:]...)
	o.run.mu.Unlock()
	if len(evs) == 0 {
		return
	}
	wire, err := encodeEvents(evs)
	if err != nil {
		o.c.rs.logger.Error("encode run events", "error", err)
		return
	}
	from := o.sent
	for _, part := range chunks(wire) {
		o.send(runMsg{Kind: "events", Run: o.run.ID, From: from, Events: part})
		from += len(part)
	}
	o.sent += len(evs)
}

func (o *owner) send(m runMsg) {
	if err := o.c.publish(o.c.runTopic(o.key), m); err != nil {
		// 镜像会发现序号不连续或超时，并结束推送让客户端重连。
		o.c.rs.logger.Warn("broadcast run events", "error", err)
	}
}

func (o *owner) control(sub *broadcast.Subscription) {
	defer close(o.ctlDone)
	for {
		for msg := range sub.C() {
			select {
			case <-o.closing:
				return // 已被新的运行取代或已过保留期：不再回应，即使订阅还没来得及关闭
			default:
			}
			var c ctlMsg
			if err := json.Unmarshal(msg.Data, &c); err != nil {
				continue
			}
			switch c.Kind {
			case "snapshot":
				o.snapshot(c.Req)
			case "steer":
				if c.Message == nil {
					continue
				}
				reply := ""
				if err := o.run.Steer(context.Background(), *c.Message); errors.Is(err, ErrFinished) {
					reply = "finished"
				} else if err != nil {
					reply = err.Error()
				}
				o.send(runMsg{Kind: "ack", Run: o.run.ID, Req: c.Req, Error: reply})
			case "notify":
				reply := ""
				if err := o.run.notify(context.Background()); errors.Is(err, ErrFinished) {
					reply = "finished"
				} else if err != nil {
					reply = err.Error()
				}
				o.send(runMsg{Kind: "ack", Run: o.run.ID, Req: c.Req, Error: reply})
			case "cancel":
				o.run.Cancel()
			}
		}
		if errors.Is(sub.Err(), broadcast.ErrClosed) {
			return // Hub 已关闭
		}
		select {
		case <-o.closing:
			return
		default:
			// 订阅因为跟不上或后端丢消息而结束：重新订阅，请求方会超时重试。
			sub = o.c.hub.Subscribe(o.c.ctlTopic(o.key))
			go func(s *broadcast.Subscription) {
				<-o.closing
				s.Close()
			}(sub)
		}
	}
}

// snapshot 广播目前为止的全部事件，供刚开始查看这次运行的实例建立镜像。
func (o *owner) snapshot(req string) {
	o.run.mu.Lock()
	evs := append([]Event(nil), o.run.events...)
	o.run.mu.Unlock()
	wire, err := encodeEvents(compact(evs))
	if err != nil {
		o.c.rs.logger.Error("encode run snapshot", "error", err)
		return
	}
	parts := chunks(wire)
	if len(parts) == 0 {
		parts = [][]wireEvent{nil}
	}
	for i, part := range parts {
		o.send(runMsg{Kind: "snapshot", Run: o.run.ID, Req: req, Next: len(evs), Events: part, Last: i == len(parts)-1})
	}
}

// finish 在运行结束后广播剩下的事件，并释放对话锁。
func (o *owner) finish() {
	close(o.stop)
	<-o.stopped
	o.release()
}

// close 停止回应控制请求。运行结束、保留期过后调用。
func (o *owner) close() {
	o.closeOnce.Do(func() { close(o.closing) })
}

// ---------- 其他实例 ----------

// mirror 是其他实例上一次运行在本实例的镜像。
type mirror struct {
	c   *cluster
	key session.Key
	run *Run
	sub *broadcast.Subscription

	next int // 下一个应当收到的事件序号

	mu   sync.Mutex
	acks map[string]chan string

	closeOnce sync.Once
	closing   chan struct{}
}

// held 报告对话的锁是否被持有，也就是对话是否正在某个实例上运行。
func (c *cluster) held(ctx context.Context, key session.Key) (bool, error) {
	lk, err := c.locker.Acquire(ctx, c.lockName(key), time.Second)
	if errors.Is(err, lock.ErrNotAcquired) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return false, lk.Release(ctx)
}

// mirror 为其他实例上正在进行的运行建立镜像；没有运行或运行所在的实例没有回应时返回 nil。
func (c *cluster) mirror(ctx context.Context, rs *Runs, key session.Key) (*Run, error) {
	held, err := c.held(ctx, key)
	if err != nil || !held {
		return nil, err
	}
	sub := c.hub.Subscribe(c.runTopic(key)) // 先订阅再请求快照，期间的事件不会漏掉
	req := uuid.NewString()
	if err := c.publish(c.ctlTopic(key), ctlMsg{Kind: "snapshot", Req: req}); err != nil {
		sub.Close()
		return nil, err
	}
	var early []runMsg
	var snap []wireEvent
	var runID string
	next := 0
	timer := time.NewTimer(replyTimeout)
	defer timer.Stop()
wait:
	for {
		select {
		case msg, ok := <-sub.C():
			if !ok {
				return nil, nil
			}
			var m runMsg
			if json.Unmarshal(msg.Data, &m) != nil {
				continue
			}
			switch {
			case m.Kind == "snapshot" && m.Req == req:
				runID = m.Run
				snap = append(snap, m.Events...)
				if m.Last {
					next = m.Next
					break wait
				}
			case m.Kind == "events":
				early = append(early, m)
			}
		case <-timer.C:
			sub.Close()
			return nil, nil
		case <-ctx.Done():
			sub.Close()
			return nil, ctx.Err()
		}
	}

	r := &Run{ID: runID, key: key, rs: rs, changed: make(chan struct{}), done: make(chan struct{})}
	m := &mirror{c: c, key: key, run: r, sub: sub, next: next, acks: map[string]chan string{}, closing: make(chan struct{})}
	r.remote = m
	r.cancel = func() { _ = c.publish(c.ctlTopic(key), ctlMsg{Kind: "cancel"}) }
	var evs []Event
	finished := false
	for _, w := range snap {
		ev, err := decodeEvent(w)
		if err != nil {
			sub.Close()
			return nil, err
		}
		evs = append(evs, ev)
		finished = finished || ev.Name == EventDone
	}
	r.append(evs, finished)
	for _, em := range early {
		if !finished {
			finished = m.apply(em)
		}
	}

	rs.mu.Lock()
	if existing, ok := rs.runs[key]; ok && (existing.remote == nil || !existing.isFinished()) {
		rs.mu.Unlock()
		m.close()
		return existing, nil
	}
	rs.runs[key] = r
	rs.mu.Unlock()
	if finished {
		m.end(rs.keep)
	} else {
		go m.loop()
	}
	return r, nil
}

// append 追加事件并唤醒读者；finished 时结束运行。
func (r *Run) append(evs []Event, finished bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.finished {
		return
	}
	r.events = append(r.events, evs...)
	if finished {
		r.finished = true
		close(r.done)
	}
	close(r.changed)
	r.changed = make(chan struct{})
}

func (m *mirror) loop() {
	idle := time.NewTimer(m.c.idle)
	defer idle.Stop()
	for {
		select {
		case msg, ok := <-m.sub.C():
			if !ok {
				select {
				case <-m.closing:
				default:
					m.broken("lost the event stream of the running instance")
				}
				return
			}
			idle.Reset(m.c.idle)
			var rm runMsg
			if json.Unmarshal(msg.Data, &rm) != nil || rm.Run != m.run.ID {
				continue
			}
			switch rm.Kind {
			case "events":
				if m.apply(rm) {
					m.end(m.run.rs.keep)
					return
				}
			case "ack":
				m.mu.Lock()
				ch := m.acks[rm.Req]
				m.mu.Unlock()
				if ch != nil {
					ch <- rm.Error
				}
			}
		case <-idle.C:
			m.broken("the running instance stopped responding")
			return
		case <-m.closing:
			return
		}
	}
}

// apply 按序号追加一批事件，返回运行是否已经结束。序号不连续时镜像失效。
func (m *mirror) apply(rm runMsg) bool {
	if rm.Run != m.run.ID {
		return false
	}
	var evs []Event
	finished := false
	for i, w := range rm.Events {
		seq := rm.From + i
		if seq < m.next {
			continue // 快照中已经包含
		}
		if seq > m.next {
			m.run.append(evs, false)
			m.broken("missed events of the running instance")
			return true
		}
		ev, err := decodeEvent(w)
		if err != nil {
			m.run.append(evs, false)
			m.broken(err.Error())
			return true
		}
		evs = append(evs, ev)
		m.next++
		finished = finished || ev.Name == EventDone
	}
	m.run.append(evs, finished)
	return finished
}

// broken 以一个错误事件结束镜像，并立即移除它：客户端重连时重新建立镜像。
func (m *mirror) broken(reason string) {
	m.run.rs.logger.Warn("mirrored run interrupted", "session_id", m.key.SessionID, "reason", reason)
	m.run.append([]Event{{ID: m.run.ID + ".lost", Name: EventError, Data: Error{reason}}}, true)
	m.end(0)
}

// end 停止接收，并在 keep 之后从 Runs 中移除镜像。
func (m *mirror) end(keep time.Duration) {
	m.close()
	rs := m.run.rs
	time.AfterFunc(keep, func() {
		rs.mu.Lock()
		if rs.runs[m.key] == m.run {
			delete(rs.runs, m.key)
		}
		rs.mu.Unlock()
	})
}

func (m *mirror) close() {
	m.closeOnce.Do(func() {
		close(m.closing)
		m.sub.Close()
	})
}

// steer 把插话转发给运行所在的实例，等待它的回执。
func (m *mirror) steer(ctx context.Context, msg model.Message) error {
	return m.request(ctx, ctlMsg{Kind: "steer", Message: &msg})
}

// request 把插话或通知转发给运行所在的实例，等待它的回执。运行已经结束时返回 ErrFinished。
func (m *mirror) request(ctx context.Context, c ctlMsg) error {
	if m.run.isFinished() {
		return ErrFinished
	}
	req := uuid.NewString()
	ch := make(chan string, 1)
	m.mu.Lock()
	m.acks[req] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.acks, req)
		m.mu.Unlock()
	}()
	c.Req = req
	if err := m.c.publish(m.c.ctlTopic(m.key), c); err != nil {
		return err
	}
	timer := time.NewTimer(replyTimeout)
	defer timer.Stop()
	select {
	case reply := <-ch:
		switch reply {
		case "":
			return nil
		case "finished":
			return ErrFinished
		default:
			return errors.New(reply)
		}
	case <-m.run.done:
		return ErrFinished
	case <-timer.C:
		return errors.Newf("chat: %s: the running instance did not reply", c.Kind)
	case <-ctx.Done():
		return ctx.Err()
	}
}
