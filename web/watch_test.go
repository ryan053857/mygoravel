package web_test

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/broadcast"
	"github.com/ryan053857/mygoravel/broadcast/broadcasttest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
)

// 设置了 Options.Credentials 时，会话被撤销并发出提醒后 SSE 立即结束，不等 30 秒的定期校验。
func TestRequireRechecksOnSignal(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	defer hub.Close()
	creds := broadcast.NewCredentials(hub)
	store := memstore.New()
	o := options()
	o.Sessions.Store = store
	o.Credentials = creds
	pages := web.New(o)
	router := chi.NewRouter()
	pages.Routes(router, web.Public).Post("/login", func(w http.ResponseWriter, r *http.Request) error {
		return pages.Login(r, identity.Subject{ID: "alice", Type: "user"})
	})
	pages.Routes(router, web.Require()).Get("/events", func(w http.ResponseWriter, r *http.Request) error {
		return web.SSE(w, r, func(s *web.Stream) error {
			<-s.Done()
			return nil
		})
	})
	srv := httptest.NewServer(router)
	defer srv.Close()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	if resp, err := c.Post(srv.URL+"/login", "", nil); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
	}
	resp, err := c.Get(srv.URL + "/events")
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("events: %v %v", resp, err)
	}
	defer resp.Body.Close()
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(ended)
	}()
	time.Sleep(50 * time.Millisecond) // 等 SSE 开始关注

	// 提醒触发复查，但会话仍然有效，连接保持。
	if err := creds.RecheckSubject(context.Background(), identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
		t.Fatal("stream ended although the session is valid")
	case <-time.After(200 * time.Millisecond):
	}

	// 会话在别处被撤销（这里直接从存储删除），再发出提醒。
	if err := store.Delete(jar.Cookies(mustURL(t, srv.URL))[0].Value); err != nil {
		t.Fatal(err)
	}
	if err := creds.RecheckSubject(context.Background(), identity.Ref{Type: "user", ID: "alice"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not ended after the session was revoked")
	}
}
