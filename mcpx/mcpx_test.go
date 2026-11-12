package mcpx_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/errors"
	"github.com/google/uuid"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/logging/logtest"
	"github.com/ryan053857/mygoravel/mcpx"
	"github.com/ryan053857/mygoravel/mcpx/mcpxtest"
	"github.com/ryan053857/mygoravel/page"
)

var errAlreadyPublished = errorx.Define(errorx.Conflict, "article.already_published")

var (
	editor = identity.Subject{Type: "editor", ID: "e1", Tenant: "acme", Permissions: []string{"articles:*"}}
	reader = identity.Subject{Type: "editor", ID: "e2", Tenant: "acme", Permissions: []string{"articles:read"}}
	// bot 是带 Scopes 的个人访问令牌：本人有全部文章权限，令牌只允许读取。
	bot = identity.Subject{Type: "editor", ID: "e1", Tenant: "acme", Permissions: []string{"articles:*"}, Scopes: []string{"articles:read"}}
)

type Author struct {
	Name string `json:"name" doc:"作者名"`
}

type PublishArgs struct {
	ID    uuid.UUID `json:"id" format:"uuid" doc:"文章 ID"`
	Note  string    `json:"note,omitempty" maxLength:"10" doc:"备注"`
	Fail  string    `json:"fail,omitempty" enum:"conflict,internal,panic,unavailable"`
	Owner *Author   `json:"owner,omitempty"`
}

type Published struct {
	ID       uuid.UUID `json:"id"`
	By       string    `json:"by"`
	Tenant   string    `json:"tenant"`
	Response string    `json:"note"`
}

type SearchArgs struct {
	Query string `json:"query" minLength:"1"`
}

func newServer(t *testing.T, logger *slog.Logger) *mcpx.Server {
	t.Helper()
	srv := mcpx.New(mcpx.Options{Name: "blog", Instructions: "博客", Logger: logger})
	mcpx.Register(srv, mcpx.Tool{
		Name: "article_publish", Title: "发布文章", Description: "发布一篇草稿",
		Permissions: []string{"articles:publish"},
	}, func(ctx context.Context, in PublishArgs) (*Published, error) {
		switch in.Fail {
		case "conflict":
			return nil, errAlreadyPublished.New()
		case "internal":
			return nil, errors.New("database password is hunter2")
		case "panic":
			panic("boom")
		case "unavailable":
			return nil, errorx.Define(errorx.Unavailable, "search.unavailable").New()
		}
		sub, _ := identity.FromContext(ctx)
		return &Published{ID: in.ID, By: sub.ID, Tenant: identity.TenantFromContext(ctx), Response: in.Note}, nil
	})
	mcpx.Register(srv, mcpx.Tool{
		Name: "article_search", Description: "搜索文章", Permissions: []string{"articles:read"}, ReadOnly: true,
	}, func(_ context.Context, in SearchArgs) ([]string, error) {
		return []string{"hello " + in.Query}, nil
	})
	mcpx.Register(srv, mcpx.Tool{Name: "ping", Description: "检查连接", ReadOnly: true},
		func(context.Context, struct{}) (map[string]string, error) {
			return map[string]string{"pong": "ok"}, nil
		})
	return srv
}

func TestToolsFilteredBySubject(t *testing.T) {
	srv := newServer(t, nil)
	for name, tc := range map[string]struct {
		sub  identity.Subject
		want []string
	}{
		"editor":         {editor, []string{"article_publish", "article_search", "ping"}},
		"reader":         {reader, []string{"article_search", "ping"}},
		"scoped token":   {bot, []string{"article_search", "ping"}},
		"no permissions": {identity.Subject{Type: "editor", ID: "e3", Tenant: "acme"}, []string{"ping"}},
	} {
		got := mcpxtest.Connect(t, srv, tc.sub).Tools()
		slices.Sort(got)
		if !slices.Equal(got, tc.want) {
			t.Errorf("%s: tools %v, want %v", name, got, tc.want)
		}
	}

	// 看不到的工具也不能调用，与不存在的工具相同。
	_, hidden := mcpxtest.Connect(t, srv, bot).Call("article_publish", map[string]any{"id": uuid.NewString()})
	_, missing := mcpxtest.Connect(t, srv, bot).Call("article_delete", map[string]any{})
	if hidden == nil || missing == nil || strings.ReplaceAll(hidden.Error(), "article_publish", "X") != strings.ReplaceAll(missing.Error(), "article_delete", "X") {
		t.Fatalf("hidden %v, missing %v", hidden, missing)
	}

	names := make([]string, 0)
	for _, tool := range srv.Tools() {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"article_publish", "article_search", "ping"}) {
		t.Fatalf("server tools %v", names)
	}
}

func TestCall(t *testing.T) {
	c := mcpxtest.Connect(t, newServer(t, nil), editor)
	id := uuid.New()
	res, err := c.Call("article_publish", map[string]any{"id": id, "note": "hi", "owner": map[string]any{"name": "alice"}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	var out Published
	if err := res.Decode(&out); err != nil {
		t.Fatal(err)
	}
	// 主体和租户经 ctx 到达工具。
	if out.ID != id || out.By != "e1" || out.Tenant != "acme" || out.Response != "hi" {
		t.Fatalf("output %+v", out)
	}
	var structured Published
	if err := json.Unmarshal(res.Structured, &structured); err != nil || structured != out {
		t.Fatalf("structured %s", res.Structured)
	}

	// 返回值不是对象时只有文本。
	res, err = c.Call("article_search", map[string]any{"query": "go"})
	if err != nil || res.IsError || res.Text != `["hello go"]` || res.Structured != nil {
		t.Fatalf("search: %v %+v", err, res)
	}
	// 没有参数的工具可以不传参数。
	res, err = c.Call("ping", nil)
	if err != nil || res.IsError || res.Text != `{"pong":"ok"}` {
		t.Fatalf("ping: %v %+v", err, res)
	}
}

func TestSchemas(t *testing.T) {
	c := mcpxtest.Connect(t, newServer(t, nil), editor)
	publish := c.Tool("article_publish")
	in, _ := json.Marshal(publish.InputSchema)
	out, _ := json.Marshal(publish.OutputSchema)
	for _, want := range []string{`"type":"object"`, `"required":["id"]`, `"description":"文章 ID"`, `"format":"uuid"`,
		`"maxLength":10`, `"additionalProperties":false`, `"$ref":"#/$defs/Author"`, `"$defs":{"Author":`, `"description":"作者名"`} {
		if !strings.Contains(string(in), want) {
			t.Errorf("input schema lacks %s: %s", want, in)
		}
	}
	if !strings.Contains(string(out), `"type":"object"`) || !strings.Contains(string(out), `"by"`) {
		t.Errorf("output schema %s", out)
	}
	a := publish.Annotations
	if publish.Title != "发布文章" || a.ReadOnlyHint || a.DestructiveHint == nil || *a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("publish annotations %+v", a)
	}
	search := c.Tool("article_search")
	if !search.Annotations.ReadOnlyHint || search.Annotations.DestructiveHint != nil || search.OutputSchema != nil {
		t.Errorf("search %+v %v", search.Annotations, search.OutputSchema)
	}
}

func TestValidation(t *testing.T) {
	called := false
	srv := mcpx.New(mcpx.Options{Name: "blog"})
	mcpx.Register(srv, mcpx.Tool{Name: "article_publish", Description: "发布"}, func(_ context.Context, in PublishArgs) (*Published, error) {
		called = true
		return &Published{}, nil
	})
	c := mcpxtest.Connect(t, srv, editor)
	for name, tc := range map[string]struct {
		args any
		want []string
	}{
		"missing id":  {map[string]any{}, []string{"id", "required"}},
		"bad uuid":    {map[string]any{"id": "42"}, []string{"id", "uuid"}},
		"too long":    {map[string]any{"id": uuid.NewString(), "note": "01234567890"}, []string{"note", "length"}},
		"bad enum":    {map[string]any{"id": uuid.NewString(), "fail": "nope"}, []string{"fail"}},
		"extra field": {map[string]any{"id": uuid.NewString(), "slug": "x"}, []string{"slug"}},
		"wrong type":  {map[string]any{"id": uuid.NewString(), "owner": "alice"}, []string{"owner", "object"}},
	} {
		res, err := c.Call("article_publish", tc.args)
		if err != nil || !res.IsError || !strings.HasPrefix(res.Text, "invalid arguments:") {
			t.Errorf("%s: %v %+v", name, err, res)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(res.Text, w) {
				t.Errorf("%s: %q lacks %q", name, res.Text, w)
			}
		}
	}
	if called {
		t.Fatal("handler called with invalid arguments")
	}
}

func TestErrors(t *testing.T) {
	rec := logtest.New()
	c := mcpxtest.Connect(t, newServer(t, rec.Logger()), editor)
	call := func(fail string) mcpxtest.Result {
		t.Helper()
		res, err := c.Call("article_publish", map[string]any{"id": uuid.NewString(), "fail": fail})
		if err != nil || !res.IsError {
			t.Fatalf("%s: %v %+v", fail, err, res)
		}
		return res
	}

	// 业务错误：错误码和消息交给模型，不记录错误日志。
	if res := call("conflict"); !strings.HasPrefix(res.Text, "article.already_published: ") {
		t.Fatalf("conflict %q", res.Text)
	}
	if rec.Has(slog.LevelError, "tool failed") {
		t.Fatal("business error logged")
	}
	// 内部错误和 panic：模型只看到“内部错误”，完整错误在日志中。
	for _, fail := range []string{"internal", "panic"} {
		res := call(fail)
		if !strings.HasPrefix(res.Text, "internal error") || strings.Contains(res.Text, "hunter2") || strings.Contains(res.Text, "boom") {
			t.Fatalf("%s %q", fail, res.Text)
		}
	}
	if res := call("unavailable"); !strings.HasPrefix(res.Text, "service temporarily unavailable") {
		t.Fatalf("unavailable %q", res.Text)
	}
	var logged []string
	for _, r := range rec.Records() {
		if r.Message == "tool failed" && r.Level == slog.LevelError && r.Attr("mcp.tool") == "article_publish" && r.Attr("component") == "mcp" {
			logged = append(logged, r.Attr("error.message").(string))
		}
	}
	if len(logged) != 3 || !strings.Contains(logged[0], "hunter2") || !strings.Contains(logged[1], "boom") {
		t.Fatalf("logged %v", logged)
	}
}

func TestHandlerAuthentication(t *testing.T) {
	srv := newServer(t, nil)
	tokens := map[string]identity.Subject{
		"editor":  editor,
		"globex":  {Type: "editor", ID: "e9", Tenant: "globex", Permissions: []string{"*"}},
		"member":  {Type: "member", ID: "m1", Tenant: "acme"},
		"nopower": {Type: "editor", ID: "e3", Tenant: "acme"},
	}
	h := mcpx.Handler(srv, mcpx.HandlerOptions{
		Validate: func(_ context.Context, token string) (identity.Subject, error) {
			if token == "broken" {
				return identity.Subject{}, errorx.Define(errorx.Unavailable, "tokens.unavailable").New()
			}
			return tokens[token], nil
		},
		Accounts:    identity.Accounts{"editor": nil},
		Tenant:      func(r *http.Request) string { return r.Header.Get("X-Tenant") },
		Permissions: []string{"articles:read"},
	})
	initialize := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`
	for name, tc := range map[string]struct {
		auth, tenant string
		status       int
	}{
		"no token":           {"", "acme", http.StatusUnauthorized},
		"basic auth":         {"Basic ZWRpdG9y", "acme", http.StatusUnauthorized},
		"unknown token":      {"Bearer nobody", "acme", http.StatusUnauthorized},
		"other account type": {"Bearer member", "acme", http.StatusUnauthorized},
		"other tenant":       {"Bearer globex", "acme", http.StatusForbidden},
		"missing permission": {"Bearer nopower", "acme", http.StatusForbidden},
		"backend down":       {"Bearer broken", "acme", http.StatusServiceUnavailable},
		"ok":                 {"bearer editor", "acme", http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(initialize))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("X-Tenant", tc.tenant)
		if tc.auth != "" {
			req.Header.Set("Authorization", tc.auth)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != tc.status {
			t.Errorf("%s: status %d, want %d: %s", name, w.Code, tc.status, w.Body)
		}
		if tc.status == http.StatusUnauthorized && w.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: no challenge", name)
		}
		if w.Code != http.StatusOK && w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s: cache-control %q", name, w.Header().Get("Cache-Control"))
		}
	}

	// 部署在反向代理之后：经本机地址到达、Host 是公开域名的请求照常处理。
	req := httptest.NewRequest(http.MethodPost, "http://blog.example.com/mcp", strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("X-Tenant", "acme")
	req.Header.Set("Authorization", "Bearer editor")
	req.RemoteAddr = "127.0.0.1:50000"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"serverInfo"`) {
		t.Fatalf("behind proxy: %d %s", w.Code, w.Body)
	}
}

func TestRegisterPanics(t *testing.T) {
	ok := func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil }
	for name, register := range map[string]func(*mcpx.Server){
		"bad name":       func(s *mcpx.Server) { mcpx.Register(s, mcpx.Tool{Name: "Publish", Description: "x"}, ok) },
		"dotted name":    func(s *mcpx.Server) { mcpx.Register(s, mcpx.Tool{Name: "article.publish", Description: "x"}, ok) },
		"no description": func(s *mcpx.Server) { mcpx.Register(s, mcpx.Tool{Name: "ping"}, ok) },
		"duplicate": func(s *mcpx.Server) {
			mcpx.Register(s, mcpx.Tool{Name: "ping", Description: "x"}, ok)
			mcpx.Register(s, mcpx.Tool{Name: "ping", Description: "x"}, ok)
		},
		"non-struct input": func(s *mcpx.Server) {
			mcpx.Register(s, mcpx.Tool{Name: "ping", Description: "x"}, func(context.Context, string) (string, error) { return "", nil })
		},
		"nil handler": func(s *mcpx.Server) {
			mcpx.Register[struct{}, struct{}](s, mcpx.Tool{Name: "ping", Description: "x"}, nil)
		},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			register(mcpx.New(mcpx.Options{Name: "blog"}))
		}()
	}
}

type ListArgs struct {
	Status string `json:"status,omitempty" default:"published" enum:"draft,published"`
	page.OffsetParams
}

func TestDefaults(t *testing.T) {
	srv := mcpx.New(mcpx.Options{Name: "blog"})
	mcpx.Register(srv, mcpx.Tool{Name: "article_list", Description: "列出文章", ReadOnly: true},
		func(_ context.Context, in ListArgs) (ListArgs, error) { return in, nil })
	c := mcpxtest.Connect(t, srv, editor)
	for args, want := range map[string]ListArgs{
		`{}`:                              {Status: "published", OffsetParams: page.OffsetParams{Page: 1, PerPage: 20}},
		`{"status":"draft","per_page":5}`: {Status: "draft", OffsetParams: page.OffsetParams{Page: 1, PerPage: 5}},
		`{"page":3,"per_page":100}`:       {Status: "published", OffsetParams: page.OffsetParams{Page: 3, PerPage: 100}},
	} {
		res, err := c.Call("article_list", json.RawMessage(args))
		var got ListArgs
		if err != nil || res.IsError || res.Decode(&got) != nil || got != want {
			t.Errorf("%s: %v %+v %+v", args, err, res, got)
		}
	}
	if res, err := c.Call("article_list", json.RawMessage(`{"per_page":101}`)); err != nil || !res.IsError {
		t.Errorf("per_page 101 accepted: %v %+v", err, res)
	}
}
