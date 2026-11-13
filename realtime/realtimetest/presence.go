package realtimetest

import (
	"context"
	"sync"
	"time"

	"github.com/go-tenon/tenon/realtime"
)

var _ realtime.PresenceStore = (*Presence)(nil)

// Presence 把在线记录保存在进程内存中，供测试使用：生产环境中，不停机升级和多实例部署时
// 同时运行的进程需要共用 redispresence 存储。
// 同一个 Presence 交给多个 Realtime，就模拟了共用存储的多个实例。
type Presence struct {
	mu       sync.Mutex
	channels map[string]map[string]entry
}

type entry struct {
	realtime.PresenceEntry
	expires time.Time
}

// NewPresence 创建内存在线记录存储。
func NewPresence() *Presence {
	return &Presence{channels: map[string]map[string]entry{}}
}

// Add 实现 realtime.PresenceStore。
func (s *Presence) Add(_ context.Context, e realtime.PresenceEntry, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	clients := s.channels[e.Channel]
	if clients == nil {
		clients = map[string]entry{}
		s.channels[e.Channel] = clients
	}
	clients[e.Client] = entry{PresenceEntry: e, expires: time.Now().Add(ttl)}
	return nil
}

// Remove 实现 realtime.PresenceStore。
func (s *Presence) Remove(_ context.Context, channel, client string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.channels[channel], client)
	if len(s.channels[channel]) == 0 {
		delete(s.channels, channel)
	}
	return nil
}

// List 实现 realtime.PresenceStore。
func (s *Presence) List(_ context.Context, channel string) ([]realtime.PresenceEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	var out []realtime.PresenceEntry
	for client, e := range s.channels[channel] {
		if !e.expires.After(now) {
			delete(s.channels[channel], client)
			continue
		}
		out = append(out, e.PresenceEntry)
	}
	if len(s.channels[channel]) == 0 {
		delete(s.channels, channel)
	}
	return out, nil
}
