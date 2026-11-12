// Package mcpxtest 在测试中以指定的主体连接 mcpx.Server，列出和调用工具。
//
//	c := mcpxtest.Connect(t, srv, editor) // editor 是 identity.Subject
//	if !slices.Contains(c.Tools(), "article_publish") { t.Fatal(c.Tools()) }
//	res, err := c.Call("article_publish", map[string]any{"id": id})
//	if err != nil || res.IsError { t.Fatal(err, res.Text) }
//
// 连接经过 mcpx.Handler：认证、按主体筛选工具、参数校验和错误转换都与线上相同。
package mcpxtest

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/mcpx"
)

// token 是测试连接使用的 bearer token。
const token = "mcpxtest"

// Client 是以一个主体连接到 Server 的客户端，测试结束时关闭。
type Client struct {
	t       testing.TB
	session *mcp.ClientSession
}

// Connect 以 sub 的身份连接 s。sub 原样作为每个请求的主体，包括 Tenant、Permissions 和 Scopes。sub.ID 不能为空。
func Connect(t testing.TB, s *mcpx.Server, sub identity.Subject) *Client {
	t.Helper()
	h := mcpx.Handler(s, mcpx.HandlerOptions{
		Validate: func(_ context.Context, got string) (identity.Subject, error) {
			if got != token {
				return identity.Subject{}, nil
			}
			return sub, nil
		},
		Accounts: identity.Accounts{subjectType(sub): nil},
		Tenant:   func(*http.Request) string { return sub.Tenant },
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   srv.URL,
		HTTPClient: &http.Client{Transport: bearer{srv.Client().Transport}},
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpxtest", Version: "0.0.0"}, nil)
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("mcpxtest: connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return &Client{t: t, session: session}
}

func subjectType(sub identity.Subject) identity.SubjectType {
	if sub.Type == "" {
		return "user"
	}
	return sub.Type
}

type bearer struct{ next http.RoundTripper }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+token)
	return b.next.RoundTrip(r)
}

// Tools 返回这个主体能看到的工具名，按服务返回的顺序。
func (c *Client) Tools() []string {
	c.t.Helper()
	var names []string
	for tool, err := range c.session.Tools(context.Background(), nil) {
		if err != nil {
			c.t.Fatalf("mcpxtest: list tools: %v", err)
		}
		names = append(names, tool.Name)
	}
	return names
}

// Tool 返回名为 name 的工具的完整定义，这个主体看不到它时返回 nil。
func (c *Client) Tool(name string) *mcp.Tool {
	c.t.Helper()
	for tool, err := range c.session.Tools(context.Background(), nil) {
		if err != nil {
			c.t.Fatalf("mcpxtest: list tools: %v", err)
		}
		if tool.Name == name {
			return tool
		}
	}
	return nil
}

// Result 是一次工具调用的结果。
type Result struct {
	// IsError 表示工具报告了错误：业务错误、参数不合法或内部错误，Text 是给模型看的说明。
	IsError bool
	// Text 是结果的文本内容；成功时是返回值的 JSON。
	Text string
	// Structured 是结构化内容，返回值不是对象时为 nil。
	Structured json.RawMessage
}

// Decode 把成功结果的 JSON 解码到 v。
func (r Result) Decode(v any) error { return json.Unmarshal([]byte(r.Text), v) }

// Call 调用工具 name。工具不存在或这个主体没有权限时返回错误；工具自己报告的错误在 Result.IsError 中。
func (c *Client) Call(name string, args any) (Result, error) {
	res, err := c.session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return Result{}, err
	}
	var text []string
	for _, content := range res.Content {
		if t, ok := content.(*mcp.TextContent); ok {
			text = append(text, t.Text)
		}
	}
	r := Result{IsError: res.IsError, Text: strings.Join(text, "\n")}
	if res.StructuredContent != nil {
		data, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return Result{}, err
		}
		r.Structured = data
	}
	return r, nil
}
