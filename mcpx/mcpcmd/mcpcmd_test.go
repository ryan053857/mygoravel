package mcpcmd_test

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/mcpx"
	"github.com/ryan053857/mygoravel/mcpx/mcpcmd"
)

var ops = identity.Subject{Type: identity.System, ID: "ops", Tenant: "acme", Permissions: []string{"articles:read"}}

type whoami struct {
	ID     string `json:"id"`
	Tenant string `json:"tenant"`
}

func newServer() *mcpx.Server {
	srv := mcpx.New(mcpx.Options{Name: "blog"})
	mcpx.Register(srv, mcpx.Tool{Name: "whoami", Title: "我是谁", Description: "返回当前主体", Permissions: []string{"articles:read"}, ReadOnly: true},
		func(ctx context.Context, _ struct{}) (whoami, error) {
			sub, _ := identity.FromContext(ctx)
			return whoami{ID: sub.ID, Tenant: identity.TenantFromContext(ctx)}, nil
		})
	mcpx.Register(srv, mcpx.Tool{Name: "article_delete", Description: "删除文章", Permissions: []string{"articles:admin"}, Destructive: true},
		func(context.Context, struct{}) (struct{}, error) { return struct{}{}, nil })
	return srv
}

func TestServe(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverSide, clientSide := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- mcpcmd.Serve(ctx, newServer(), ops, serverSide) }()

	session, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, tool.Name)
	}
	// 权限仍按工具的 Permissions 检查。
	if !slices.Equal(names, []string{"whoami"}) {
		t.Fatalf("tools %v", names)
	}
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatal(err, res)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; text != `{"id":"ops","tenant":"acme"}` {
		t.Fatalf("whoami %s", text)
	}
	_ = session.Close()
	if err := <-done; err != nil {
		t.Fatalf("serve: %v", err)
	}
}

func TestToolsCommand(t *testing.T) {
	cmd := mcpcmd.New(newServer(), ops)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"tools"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "NAME") ||
		!strings.Contains(lines[1], "article_delete") || !strings.Contains(lines[1], "articles:admin") || !strings.Contains(lines[1], "destructive") ||
		!strings.Contains(lines[2], "whoami") || !strings.Contains(lines[2], "read-only") || !strings.Contains(lines[2], "我是谁") {
		t.Fatalf("output:\n%s", out.String())
	}
}
