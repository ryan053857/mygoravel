package rest_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/broadcast"
	"github.com/go-tenon/tenon/broadcast/broadcasttest"
	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/httpx/rest"
	"github.com/go-tenon/tenon/identity"
)

// 设置了 Options.Credentials 时，撤销 token 并发出提醒后 SSE 立即结束，不等 30 秒的定期校验。
func TestRequireBearerRechecksOnSignal(t *testing.T) {
	hub := broadcast.New(broadcasttest.NewBackend(), nil)
	defer hub.Close()
	creds := broadcast.NewCredentials(hub)
	var revoked atomic.Bool
	validate := func(_ context.Context, token string) (identity.Subject, error) {
		if token != "t" || revoked.Load() {
			return identity.Subject{}, errorx.Define(errorx.Unauthenticated, "tokens.revoked").New()
		}
		return identity.Subject{ID: "alice", Type: "user", Credential: "token-1"}, nil
	}
	api := rest.New(rest.Options{Accounts: identity.Accounts{"user": nil}, Credentials: creds})
	op := huma.Operation{OperationID: "events", Method: http.MethodGet, Path: "/events"}
	rest.RequireBearer(api, validate)(&op)
	rest.RegisterSSE(api, op, map[string]any{}, func(context.Context, *struct{}) (*huma.StreamResponse, error) {
		return rest.SSE(func(s *httpx.Stream) error {
			if err := s.Send(httpx.Event{}, "ready"); err != nil {
				return err
			}
			<-s.Done()
			return nil
		}), nil
	})
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/events", nil)
	req.Header.Set("Authorization", "Bearer t")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	ended := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(ended)
	}()
	time.Sleep(50 * time.Millisecond) // 等 SSE 开始关注

	// 提醒触发复查，但 token 仍然有效，连接保持。
	if err := creds.RecheckCredential(context.Background(), "token-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
		t.Fatal("stream ended although the token is valid")
	case <-time.After(200 * time.Millisecond):
	}

	revoked.Store(true)
	if err := creds.RecheckCredential(context.Background(), "token-1"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ended:
	case <-time.After(2 * time.Second):
		t.Fatal("stream not ended after the token was revoked")
	}
}
