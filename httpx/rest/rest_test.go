package rest_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/httpx/rest"
)

type pingIn struct{}
type pingOut struct {
	Body struct {
		Message string `json:"message"`
	}
}

type echoIn struct {
	Body struct {
		Secret string `json:"secret" minLength:"2"`
	}
}
type echoOut struct {
	Body struct {
		OK bool `json:"ok"`
	}
}

func TestOpenAPIDoesNotCallNewHandler(t *testing.T) {
	called := false
	api := rest.New(rest.Options{Title: "doc", Mode: rest.ModeOpenAPI})
	rest.Register(api, huma.Operation{Security: []map[string][]string{},
		OperationID: "ping",
		Method:      http.MethodGet,
		Path:        "/ping",
	}, func() func(context.Context, *pingIn) (*pingOut, error) {
		called = true
		return func(context.Context, *pingIn) (*pingOut, error) {
			t.Fatal("handler body should not run in openapi mode")
			return nil, nil
		}
	})
	if called {
		t.Fatal("newHandler must not be called in ModeOpenAPI")
	}
	doc, err := api.OpenAPIJSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), "/ping") {
		t.Fatalf("openapi missing /ping: %s", doc)
	}
}

func TestInstanceTransformersIsolated(t *testing.T) {
	var seenA, seenB int
	markA := func(ctx huma.Context, status string, v any) (any, error) {
		seenA++
		return v, nil
	}
	markB := func(ctx huma.Context, status string, v any) (any, error) {
		seenB++
		return v, nil
	}
	a := rest.New(rest.Options{Title: "a", Transformers: []huma.Transformer{markA}})
	b := rest.New(rest.Options{Title: "b", Transformers: []huma.Transformer{markB}})
	rest.Register(a, huma.Operation{Security: []map[string][]string{}, OperationID: "a-ping", Method: http.MethodGet, Path: "/ping"}, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(context.Context, *pingIn) (*pingOut, error) {
			out := &pingOut{}
			out.Body.Message = "a"
			return out, nil
		}
	})
	rest.Register(b, huma.Operation{Security: []map[string][]string{}, OperationID: "b-ping", Method: http.MethodGet, Path: "/ping"}, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(context.Context, *pingIn) (*pingOut, error) {
			out := &pingOut{}
			out.Body.Message = "b"
			return out, nil
		}
	})

	ra := httptest.NewRecorder()
	a.Handler().ServeHTTP(ra, httptest.NewRequest(http.MethodGet, "/ping", nil))
	rb := httptest.NewRecorder()
	b.Handler().ServeHTTP(rb, httptest.NewRequest(http.MethodGet, "/ping", nil))
	if ra.Code != 200 || rb.Code != 200 {
		t.Fatalf("status a=%d b=%d", ra.Code, rb.Code)
	}
	if seenA != 1 || seenB != 1 {
		t.Fatalf("transformer crosstalk or miss: a=%d b=%d", seenA, seenB)
	}
}

func TestPingServeAndErrorMapping(t *testing.T) {
	errDenied := errorx.Define(errorx.PermissionDenied, "demo.denied")
	api := rest.New(rest.Options{Title: "ping"})
	rest.Register(api, huma.Operation{Security: []map[string][]string{},
		OperationID: "ping",
		Method:      http.MethodGet,
		Path:        "/ping",
	}, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(ctx context.Context, _ *pingIn) (*pingOut, error) {
			out := &pingOut{}
			out.Body.Message = "pong"
			return out, nil
		}
	})
	rest.Register(api, huma.Operation{Security: []map[string][]string{},
		OperationID: "deny",
		Method:      http.MethodGet,
		Path:        "/deny",
	}, func() func(context.Context, *pingIn) (*pingOut, error) {
		return func(context.Context, *pingIn) (*pingOut, error) {
			return nil, errDenied.New()
		}
	})
	rest.Register(api, huma.Operation{Security: []map[string][]string{},
		OperationID: "echo",
		Method:      http.MethodPost,
		Path:        "/echo",
	}, func() func(context.Context, *echoIn) (*echoOut, error) {
		return func(context.Context, *echoIn) (*echoOut, error) {
			out := &echoOut{}
			out.Body.OK = true
			return out, nil
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- httpx.Serve(ctx, httpx.Config{ShutdownTimeout: time.Second}, ln, api.Handler(), httpx.ServeOptions{})
	}()
	base := "http://" + ln.Addr().String()

	resp, err := http.Get(base + "/ping")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "pong") {
		t.Fatalf("ping: %d %s", resp.StatusCode, body)
	}

	resp, err = http.Get(base + "/deny")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("deny status=%d body=%s", resp.StatusCode, body)
	}
	var errBody map[string]any
	_ = json.Unmarshal(body, &errBody)
	if errBody["code"] != "demo.denied" {
		t.Fatalf("code=%v body=%s", errBody["code"], body)
	}

	req, _ := http.NewRequest(http.MethodPost, base+"/echo", strings.NewReader(`{"secret":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("validation status=%d body=%s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"location":"body.secret"`) {
		t.Fatalf("validation missing location: %s", body)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown timeout")
	}
}
