package rest_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/httpx"
	"github.com/go-tenon/tenon/httpx/rest"
)

type countIn struct {
	N int `query:"n"`
}

type tick struct {
	N int `json:"n"`
}

func TestRegisterSSE(t *testing.T) {
	api := rest.New(rest.Options{})
	op := huma.Operation{OperationID: "count", Method: http.MethodGet, Path: "/count"}
	rest.Public()(&op)
	rest.RegisterSSE(api, op, map[string]any{"tick": tick{}}, func(_ context.Context, in *countIn) (*huma.StreamResponse, error) {
		if in.N <= 0 {
			return nil, errorx.Define(errorx.NotFound, "count.empty").New()
		}
		return rest.SSE(func(s *httpx.Stream) error {
			for i := range in.N {
				if err := s.SendJSON(httpx.Event{Name: "tick"}, tick{i}); err != nil {
					return err
				}
			}
			return nil
		}), nil
	})
	srv := httptest.NewServer(api.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/count?n=2")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("content type %q", resp.Header.Get("Content-Type"))
	}
	if want := "event: tick\ndata: {\"n\":0}\n\nevent: tick\ndata: {\"n\":1}\n\n"; string(body) != want {
		t.Fatalf("body %q", body)
	}

	// 开始推送之前的错误照常返回 Problem Details。
	resp, err = http.Get(srv.URL + "/count?n=0")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/problem+json") {
		t.Fatalf("status %d, content type %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	doc, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), `"text/event-stream"`) || !strings.Contains(string(doc), `"Event tick"`) {
		t.Fatalf("openapi lacks event stream: %s", doc)
	}
}
