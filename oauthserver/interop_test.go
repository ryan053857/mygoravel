package oauthserver_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// handlerTransport 把请求交给 h，不监听端口：官方 SDK 的客户端经它访问授权服务器和 MCP 服务。
type handlerTransport struct{ h http.Handler }

func (t handlerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.h.ServeHTTP(w, r)
	return w.Result(), nil
}

// TestOfficialSDKClient 用 MCP 官方 Go SDK 的 OAuth 客户端走完整个流程：401 → 受保护资源元数据 → 授权服务器元数据 →
// 按 Client ID Metadata Document 授权（浏览器中的同意由已登录的 webtest 客户端完成）→ 换取令牌 → 调用工具。
func TestOfficialSDKClient(t *testing.T) {
	t.Parallel()
	e := setup(t)
	e.browser.Login(e.pages, editor)
	client := &http.Client{Transport: handlerTransport{e.router}}

	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		ClientIDMetadataDocumentConfig: &auth.ClientIDMetadataDocumentConfig{URL: cimd},
		RedirectURL:                    callback,
		Client:                         client,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, err := url.Parse(args.URL)
			if err != nil {
				return nil, err
			}
			if res := e.browser.Get(u.RequestURI()); res.Code != http.StatusOK {
				t.Fatalf("consent page: %d %s", res.Code, res.Text())
			}
			back := e.approve(t, u.Query(), "allow")
			return &auth.AuthorizationResult{Code: back.Query().Get("code"), State: back.Query().Get("state"), Iss: back.Query().Get("iss")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "interop", Version: "1"}, nil).Connect(ctx,
		&mcp.StreamableClientTransport{Endpoint: resource, HTTPClient: client, OAuthHandler: handler}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer session.Close()
	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "whoami", Arguments: map[string]any{}})
	if err != nil || res.IsError {
		t.Fatalf("call: %v %+v", err, res)
	}
	if text := res.Content[0].(*mcp.TextContent).Text; !strings.Contains(text, `"id":"e1"`) {
		t.Fatalf("whoami %s", text)
	}
}
