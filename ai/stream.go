package ai

import "github.com/ryan053857/mygoravel/httpx"

// Stream 把运行的事件以 JSON 写到 SSE 连接，直到运行结束或客户端断开。
// 客户端重连时从 Last-Event-ID 之后接着发送。
func Stream(s *httpx.Stream, r *Run) error {
	for ev := range r.Events(s.Context(), s.LastEventID()) {
		if err := s.SendJSON(httpx.Event{Name: ev.Name, ID: ev.ID}, ev.Data); err != nil {
			return err
		}
	}
	return nil
}
