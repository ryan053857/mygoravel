package oauthserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/ryan053857/mygoravel/oauthserver"
)

func withDCR(o *oauthserver.Options) { o.DynamicRegistration = true }

func (e *env) register(t *testing.T, body string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, issuer+oauthserver.RegisterPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestDynamicRegistration(t *testing.T) {
	t.Parallel()
	e := setupWith(t, withDCR)
	var meta map[string]any
	_ = json.Unmarshal(e.do(http.MethodGet, oauthserver.MetadataPath, "", nil).Body.Bytes(), &meta)
	if meta["registration_endpoint"] != issuer+"/oauth/register" {
		t.Fatalf("metadata %v", meta)
	}

	status, out := e.register(t, `{"client_name":"Some Agent","redirect_uris":["https://agent.example.com/cb"],"token_endpoint_auth_method":"none","grant_types":["authorization_code","refresh_token"]}`)
	id, _ := out["client_id"].(string)
	if status != http.StatusCreated || !strings.HasPrefix(id, "dcr_") || out["token_endpoint_auth_method"] != "none" || out["client_secret"] != nil {
		t.Fatalf("register: %d %v", status, out)
	}

	// 用注册得到的 client_id 走完授权。
	q := authorizeQuery(func(q url.Values) {
		q.Set("client_id", id)
		q.Set("redirect_uri", "https://agent.example.com/cb")
	})
	if status, c := e.consent(e.login, e.request(t, q)); status != http.StatusOK || c["client_name"] != "Some Agent" {
		t.Fatalf("consent: %d %v", status, c)
	}
	code := e.approve(t, q, true).Query().Get("code")
	form := tokenForm(code)
	form.Set("client_id", id)
	form.Set("redirect_uri", "https://agent.example.com/cb")
	if status, body := e.exchange(t, form); status != http.StatusOK {
		t.Fatalf("token: %d %v", status, body)
	}

	for name, body := range map[string]string{
		"not json":       `nope`,
		"no redirects":   `{"client_name":"x"}`,
		"http redirect":  `{"redirect_uris":["http://agent.example.com/cb"]}`,
		"secret":         `{"redirect_uris":["https://agent.example.com/cb"],"token_endpoint_auth_method":"client_secret_basic"}`,
		"implicit only":  `{"redirect_uris":["https://agent.example.com/cb"],"grant_types":["implicit"]}`,
		"multiline name": `{"redirect_uris":["https://agent.example.com/cb"],"client_name":"a\nb"}`,
	} {
		if status, out := e.register(t, body); status != http.StatusBadRequest || out["error"] == nil {
			t.Errorf("%s: %d %v", name, status, out)
		}
	}
	// 本机的 http 回调地址可以注册。
	if status, _ := e.register(t, `{"redirect_uris":["http://127.0.0.1/callback"]}`); status != http.StatusCreated {
		t.Fatalf("loopback: %d", status)
	}

	// 长期没有用于授权的注册被清理，之后的授权认不出它。
	if _, err := e.db.NewUpdate().Table("oauth_clients").Set("last_used_at = ?", time.Now().Add(-100*24*time.Hour)).Where("true").Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n, err := e.srv.CleanupClients(90*24*time.Hour)(context.Background(), 10); err != nil || n != 2 {
		t.Fatalf("cleanup: %d %v", n, err)
	}
	if u := e.authorize(t, q); u.Query().Get("error") != "oauth.invalid_client" {
		t.Fatalf("after cleanup: %s", u)
	}
}

func TestDynamicRegistrationDisabled(t *testing.T) {
	t.Parallel()
	e := setup(t)
	var meta map[string]any
	_ = json.Unmarshal(e.do(http.MethodGet, oauthserver.MetadataPath, "", nil).Body.Bytes(), &meta)
	if _, ok := meta["registration_endpoint"]; ok {
		t.Fatalf("metadata %v", meta)
	}
	if status, _ := e.register(t, `{"redirect_uris":["https://agent.example.com/cb"]}`); status == http.StatusCreated {
		t.Fatal("registered while disabled")
	}
}

// TestOfficialSDKClientWithDCR 用官方 SDK 的客户端走动态注册：它不提供 Client ID Metadata Document，只能注册。
func TestOfficialSDKClientWithDCR(t *testing.T) {
	t.Parallel()
	e := setupWith(t, withDCR)
	client := &http.Client{Transport: handlerTransport{e.router}}
	handler, err := auth.NewAuthorizationCodeHandler(&auth.AuthorizationCodeHandlerConfig{
		DynamicClientRegistrationConfig: &auth.DynamicClientRegistrationConfig{Metadata: &oauthex.ClientRegistrationMetadata{
			ClientName: "SDK Agent", RedirectURIs: []string{"http://localhost:7777/callback"}, TokenEndpointAuthMethod: "none",
		}},
		RedirectURL: "http://localhost:7777/callback",
		Client:      client,
		AuthorizationCodeFetcher: func(_ context.Context, args *auth.AuthorizationArgs) (*auth.AuthorizationResult, error) {
			u, err := url.Parse(args.URL)
			if err != nil {
				return nil, err
			}
			back := e.approve(t, u.Query(), true)
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
	if err != nil || res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, `"id":"e1"`) {
		t.Fatalf("call: %v %+v", err, res)
	}
}
