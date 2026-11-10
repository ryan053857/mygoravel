package app

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/a-h/templ"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/poc/htmx4/app/views"
	"github.com/ryan053857/mygoravel/poc/htmx4/web"
)

// event 是推给订阅者的一条消息。
type event struct {
	c  templ.Component
	id string
}

type room struct {
	msgs   []views.Message
	subs   map[chan event]struct{}
	typing map[string]*time.Timer
}

// Hub 是单实例内存广播：每个房间保存全部消息，供 Last-Event-ID 补发。
type Hub struct {
	// Connections 统计建立过的流连接数，用于判断是否发生了重连。
	Connections atomic.Int64

	mu    sync.Mutex
	seq   int
	rooms map[string]*room
	kick  chan struct{}
}

func NewHub() *Hub { return &Hub{rooms: map[string]*room{}, kick: make(chan struct{})} }

func (h *Hub) room(name string) *room {
	r, ok := h.rooms[name]
	if !ok {
		r = &room{subs: map[chan event]struct{}{}, typing: map[string]*time.Timer{}}
		h.rooms[name] = r
	}
	return r
}

// KickAll 断开所有流，模拟实例重启或网络中断。
func (h *Hub) KickAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	close(h.kick)
	h.kick = make(chan struct{})
}

// Subscribe 返回订阅通道和 lastID 之后的消息。
func (h *Hub) Subscribe(name, lastID string) (chan event, []views.Message, <-chan struct{}, func()) {
	h.Connections.Add(1)
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(name)
	ch := make(chan event, 32)
	r.subs[ch] = struct{}{}
	var missed []views.Message
	if lastID != "" {
		last, _ := strconv.Atoi(lastID)
		for _, m := range r.msgs {
			if id, _ := strconv.Atoi(m.ID); id > last {
				missed = append(missed, m)
			}
		}
	}
	return ch, missed, h.kick, func() {
		h.mu.Lock()
		delete(r.subs, ch)
		h.mu.Unlock()
	}
}

func (h *Hub) broadcast(r *room, ev event) {
	for ch := range r.subs {
		select {
		case ch <- ev:
		default: // 慢订阅者丢消息，重连后靠 Last-Event-ID 补发
		}
	}
}

func (h *Hub) Post(name, author, body string) views.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(name)
	h.seq++
	m := views.Message{ID: strconv.Itoa(h.seq), Author: author, Body: body}
	r.msgs = append(r.msgs, m)
	if t, ok := r.typing[author]; ok {
		t.Stop()
		delete(r.typing, author)
	}
	h.broadcast(r, event{c: views.NewMessage(m), id: m.ID})
	h.broadcast(r, event{c: views.Typing(typingNames(r))})
	return m
}

// Typing 标记 author 正在输入，3 秒没有新的输入后自动清除。
func (h *Hub) Typing(name, author string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.room(name)
	if t, ok := r.typing[author]; ok {
		t.Reset(3 * time.Second)
		return
	}
	r.typing[author] = time.AfterFunc(3*time.Second, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(r.typing, author)
		h.broadcast(r, event{c: views.Typing(typingNames(r))})
	})
	h.broadcast(r, event{c: views.Typing(typingNames(r))})
}

func typingNames(r *room) string {
	names := make([]string, 0, len(r.typing))
	for n := range r.typing {
		names = append(names, n)
	}
	slices.Sort(names)
	return strings.Join(names, "、")
}

func (h *Hub) Messages(name string) []views.Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.room(name).msgs)
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func (a *App) room(w http.ResponseWriter, r *http.Request) error {
	name := chi.URLParam(r, "room")
	return web.Render(w, r, http.StatusOK, views.Room(name, a.Hub.Messages(name), atoi(r.URL.Query().Get("timeout"))))
}

func (a *App) postMessage(w http.ResponseWriter, r *http.Request) error {
	sub, _ := identity.FromContext(r.Context())
	if body := strings.TrimSpace(r.PostFormValue("body")); body != "" {
		a.Hub.Post(chi.URLParam(r, "room"), sub.ID, body)
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *App) typing(w http.ResponseWriter, r *http.Request) error {
	sub, _ := identity.FromContext(r.Context())
	a.Hub.Typing(chi.URLParam(r, "room"), sub.ID)
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// events 是房间的长连接：先补发 Last-Event-ID 之后的消息，再推送新事件，排空开始时结束。
func (a *App) events(w http.ResponseWriter, r *http.Request) error {
	ch, missed, kicked, unsubscribe := a.Hub.Subscribe(chi.URLParam(r, "room"), r.Header.Get("Last-Event-ID"))
	defer unsubscribe()
	ctx, cancel := a.Drain.Context(r.Context())
	defer cancel()
	s, err := web.NewStream(w, r)
	if err != nil {
		return err
	}
	go s.Heartbeat(ctx, 15*time.Second)
	for _, m := range missed {
		if err := s.Send(views.NewMessage(m), web.Event{ID: m.ID}); err != nil {
			return nil
		}
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-kicked:
			return nil
		case ev := <-ch:
			if err := s.Send(ev.c, web.Event{ID: ev.id}); err != nil {
				return nil
			}
		}
	}
}
