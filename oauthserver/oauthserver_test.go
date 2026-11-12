package oauthserver_test

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/mcpx"
	"github.com/ryan053857/mygoravel/oauthserver"
	"github.com/ryan053857/mygoravel/outbound/outboundtest"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/webtest"
)

const (
	issuer      = webtest.Origin
	resource    = webtest.Origin + "/mcp"
	cimd        = "https://client.example.com/oauth/client.json"
	cimdPattern = "GET " + cimd
	callback    = "https://client.example.com/callback"
	verifier    = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXkdBjftJeZ4CVP"
)

var editor = identity.Subject{Type: "editor", ID: "e1"}

func challenge(v string) string {
	sum := sha256.Sum256([]byte(v))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// migrations 合并 apitoken 和 oauthserver 的迁移。
func migrations(t *testing.T) fs.FS {
	t.Helper()
	merged := fstest.MapFS{}
	for prefix, src := range map[string]fs.FS{"1": apitoken.Migrations(), "2": oauthserver.Migrations()} {
		err := fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(src, p)
			dir, name := path.Split(p)
			merged[dir+prefix+name] = &fstest.MapFile{Data: data}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return merged
}

func textComponent(format string, args ...any) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := io.WriteString(w, html.EscapeString(fmt.Sprintf(format, args...)))
		return err
	})
}

type env struct {
	db      *bun.DB
	tokens  *apitoken.Store
	fake    *outboundtest.Transport
	pages   *web.Pages
	router  chi.Router
	browser *webtest.Client
	srv     *oauthserver.Server
}

func setup(t *testing.T) *env {
	t.Helper()
	return setupWith(t, nil)
}

func setupWith(t *testing.T, edit func(*oauthserver.Options)) *env {
	t.Helper()
	db := dbtest.Open(t, migrations(t))
	tokens := apitoken.NewStore(db, apitoken.Config{Lifetime: time.Hour})
	fake := outboundtest.New(t)
	fake.On(cimdPattern).Reply(outboundtest.JSON(200, map[string]any{
		"client_id": cimd, "client_name": "Claude", "redirect_uris": []string{callback},
		"token_endpoint_auth_method": "none",
	}))
	var cfg session.Config
	cfg.SetDefaults()
	cfg.Cookie.Secure = false
	editors := func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
		sub.Permissions = []string{"articles:*"}
		return sub, nil
	}
	pages := web.New(web.Options{
		Accounts:  identity.Accounts{"editor": editors},
		Sessions:  session.New(cfg, memstore.New()),
		Login:     "/login",
		ErrorPage: func(status int, msg string) templ.Component { return textComponent("error %d %s", status, msg) },
	})
	opts := oauthserver.Options{
		Issuer:     issuer,
		Resources:  []string{resource},
		Scopes:     []oauthserver.Scope{{Name: "articles:read", Description: "查看文章"}, {Name: "articles:write", Description: "修改文章"}},
		Clients:    []oauthserver.Client{{ID: "agent", Name: "内部 agent", RedirectURIs: []string{"http://localhost/callback"}}},
		Brand:      "Blog",
		Account:    func(_ context.Context, sub identity.Subject) string { return sub.ID + "@example.com" },
		HTTPClient: fake.Client(),
		Logger:     slog.New(slog.DiscardHandler),
	}
	if edit != nil {
		edit(&opts)
	}
	srv := oauthserver.New(db, tokens, opts)
	router := chi.NewRouter()
	srv.Routes(router, pages)

	tools := mcpx.New(mcpx.Options{Name: "blog"})
	mcpx.Register(tools, mcpx.Tool{Name: "whoami", Description: "当前主体", Permissions: []string{"articles:read"}, ReadOnly: true},
		func(ctx context.Context, _ struct{}) (map[string]any, error) {
			sub, _ := identity.FromContext(ctx)
			return map[string]any{"id": sub.ID, "scopes": sub.Scopes}, nil
		})
	res := srv.Resource(resource)
	router.Handle(res.MetadataPath(), mcpx.ResourceMetadata(res))
	router.Handle("/mcp", mcpx.Handler(tools, mcpx.HandlerOptions{
		Validate: tokens.ValidateFor(res.URL), Accounts: identity.Accounts{"editor": editors}, Resource: &res,
	}))
	return &env{db: db, tokens: tokens, fake: fake, pages: pages, router: router, browser: webtest.New(t, router), srv: srv}
}

func authorizeQuery(edit func(url.Values)) url.Values {
	q := url.Values{
		"response_type": {"code"}, "client_id": {cimd}, "redirect_uri": {callback}, "state": {"xyz"},
		"code_challenge": {challenge(verifier)}, "code_challenge_method": {"S256"},
		"scope": {"articles:read"}, "resource": {resource},
	}
	if edit != nil {
		edit(q)
	}
	return q
}

// approve 以已登录的用户同意 q 的授权，返回跳回客户端的地址。
func (e *env) approve(t *testing.T, q url.Values, decision string) *url.URL {
	t.Helper()
	form := url.Values{}
	for k, v := range q {
		form[k] = v
	}
	form.Set("decision", decision)
	res := e.browser.Post(oauthserver.AuthorizePath, form)
	if res.Code != http.StatusFound {
		t.Fatalf("decide: %d %s", res.Code, res.Text())
	}
	u, err := url.Parse(res.Redirect())
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func (e *env) exchange(t *testing.T, form url.Values) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, issuer+oauthserver.TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("token response cache-control %q", w.Header().Get("Cache-Control"))
	}
	return w.Code, body
}

func tokenForm(code string) url.Values {
	return url.Values{
		"grant_type": {"authorization_code"}, "code": {code}, "client_id": {cimd},
		"redirect_uri": {callback}, "code_verifier": {verifier}, "resource": {resource},
	}
}

// callTool 以 token 调用 MCP 工具 whoami，返回状态码、WWW-Authenticate 和响应体。
func (e *env) callTool(t *testing.T, token string) (int, string, string) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"whoami","arguments":{}}}`
	req := httptest.NewRequest(http.MethodPost, resource, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2025-06-18")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w.Code, w.Header().Get("WWW-Authenticate"), w.Body.String()
}

func TestFlow(t *testing.T) {
	e := setup(t)

	// 客户端先访问 MCP：401 告诉它受保护资源元数据的地址，元数据指向授权服务器。
	code, challengeHeader, _ := e.callTool(t, "")
	if code != http.StatusUnauthorized || challengeHeader != `Bearer resource_metadata="`+issuer+`/.well-known/oauth-protected-resource/mcp"` {
		t.Fatalf("unauthenticated: %d %q", code, challengeHeader)
	}
	prm := e.browser.Get("/.well-known/oauth-protected-resource/mcp")
	var meta map[string]any
	if err := json.Unmarshal(prm.Body.Bytes(), &meta); err != nil || meta["resource"] != resource ||
		fmt.Sprint(meta["authorization_servers"]) != "["+issuer+"]" || fmt.Sprint(meta["scopes_supported"]) != "[articles:read articles:write]" {
		t.Fatalf("resource metadata %s", prm.Text())
	}
	as := e.browser.Get(oauthserver.MetadataPath)
	if err := json.Unmarshal(as.Body.Bytes(), &meta); err != nil || meta["issuer"] != issuer ||
		meta["token_endpoint"] != issuer+"/oauth/token" || meta["client_id_metadata_document_supported"] != true ||
		fmt.Sprint(meta["token_endpoint_auth_methods_supported"]) != "[none]" || fmt.Sprint(meta["code_challenge_methods_supported"]) != "[S256]" {
		t.Fatalf("server metadata %s", as.Text())
	}

	// 没有登录时先去登录页，登录后回到授权页。
	q := authorizeQuery(nil)
	res := e.browser.Get(oauthserver.AuthorizePath + "?" + q.Encode())
	if res.Code != http.StatusSeeOther && res.Code != http.StatusFound || !strings.HasPrefix(res.Redirect(), "/login?") {
		t.Fatalf("not logged in: %d %q", res.Code, res.Redirect())
	}

	// 登录后看到同意页：应用、账号、范围和跳转去向；不能被嵌进其他网站的框架。
	e.browser.Login(e.pages, editor)
	res = e.browser.Get(oauthserver.AuthorizePath + "?" + q.Encode())
	page := res.Text()
	if res.Code != http.StatusOK || !strings.Contains(page, "Claude") || !strings.Contains(page, "e1@example.com") ||
		!strings.Contains(page, "查看文章") || strings.Contains(page, "修改文章") || !strings.Contains(page, "client.example.com") ||
		res.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("consent: %d %s", res.Code, page)
	}

	// 同意：带着授权码、state 和 iss 跳回客户端。
	back := e.approve(t, q, "allow")
	if back.Scheme+"://"+back.Host+back.Path != callback || back.Query().Get("state") != "xyz" ||
		back.Query().Get("iss") != issuer || back.Query().Get("code") == "" {
		t.Fatalf("redirect %s", back)
	}

	// 授权码换令牌。
	status, body := e.exchange(t, tokenForm(back.Query().Get("code")))
	if status != http.StatusOK || body["token_type"] != "Bearer" || body["scope"] != "articles:read" || body["access_token"] == "" {
		t.Fatalf("token: %d %v", status, body)
	}
	token := body["access_token"].(string)

	// 用令牌调用 MCP：以用户的身份，权限是用户本人的权限与同意的范围的交集。
	code, _, out := e.callTool(t, token)
	if code != http.StatusOK || !strings.Contains(out, `\"id\":\"e1\"`) || !strings.Contains(out, `articles:read`) {
		t.Fatalf("mcp call: %d %s", code, out)
	}
	// 令牌不能用于 REST（Validate）。
	if _, err := e.tokens.Validate(context.Background(), token); err == nil {
		t.Fatal("OAuth token accepted by Validate")
	}
	// 授权码只能用一次。
	if status, body := e.exchange(t, tokenForm(back.Query().Get("code"))); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("reused code: %d %v", status, body)
	}
	// 用户在“已连接的应用”中看到它。
	list, err := e.tokens.List(context.Background(), editor)
	if err != nil || len(list) != 1 || list[0].ClientName != "Claude" || list[0].Kind != apitoken.KindOAuth {
		t.Fatalf("list %+v %v", list, err)
	}

	// 客户端撤销令牌（RFC 7009）后不能再用。
	req := httptest.NewRequest(http.MethodPost, issuer+oauthserver.RevokePath, strings.NewReader(url.Values{"token": {token}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("revoke: %d", w.Code)
	}
	if code, _, _ := e.callTool(t, token); code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", code)
	}
}

func TestTokenErrors(t *testing.T) {
	e := setup(t)
	e.browser.Login(e.pages, editor)
	issue := func() string {
		return e.approve(t, authorizeQuery(nil), "allow").Query().Get("code")
	}
	for name, tc := range map[string]struct {
		edit func(url.Values)
		want string
	}{
		"wrong verifier": {func(f url.Values) { f.Set("code_verifier", strings.Repeat("a", 43)) }, "invalid_grant"},
		"wrong client":   {func(f url.Values) { f.Set("client_id", "agent") }, "invalid_grant"},
		"wrong redirect": {func(f url.Values) { f.Set("redirect_uri", "https://client.example.com/other") }, "invalid_grant"},
		"wrong resource": {func(f url.Values) { f.Set("resource", "https://other.example.com/mcp") }, "invalid_target"},
		"no verifier":    {func(f url.Values) { f.Del("code_verifier") }, "invalid_request"},
		"refresh grant":  {func(f url.Values) { f.Set("grant_type", "refresh_token") }, "unsupported_grant_type"},
		"unknown code":   {func(f url.Values) { f.Set("code", "nope") }, "invalid_grant"},
	} {
		form := tokenForm(issue())
		tc.edit(form)
		if status, body := e.exchange(t, form); status != http.StatusBadRequest || body["error"] != tc.want {
			t.Errorf("%s: %d %v", name, status, body)
		}
	}

	// 过期的授权码。
	c := issue()
	if _, err := e.db.NewUpdate().Table("oauth_codes").Set("expires_at = ?", time.Now().Add(-time.Second)).Where("true").Exec(context.Background()); err != nil {
		t.Fatal(err)
	}
	if status, body := e.exchange(t, tokenForm(c)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("expired: %d %v", status, body)
	}
}

func TestAuthorizeErrors(t *testing.T) {
	e := setup(t)
	e.browser.Login(e.pages, editor)

	// 不能确认回调地址属于这个客户端：呈现错误页，不跳回。
	for name, edit := range map[string]func(url.Values){
		"unknown client":    func(q url.Values) { q.Set("client_id", "nobody") },
		"http metadata URL": func(q url.Values) { q.Set("client_id", "http://client.example.com/oauth/client.json") },
		"foreign redirect":  func(q url.Values) { q.Set("redirect_uri", "https://evil.example.com/callback") },
		"no redirect":       func(q url.Values) { q.Del("redirect_uri") },
	} {
		res := e.browser.Get(oauthserver.AuthorizePath + "?" + authorizeQuery(edit).Encode())
		if res.Code != http.StatusUnprocessableEntity || res.Redirect() != "" {
			t.Errorf("%s: %d %s", name, res.Code, res.Text())
		}
	}

	// 其他问题带着错误跳回客户端。
	for name, tc := range map[string]struct {
		edit func(url.Values)
		want string
	}{
		"token response": {func(q url.Values) { q.Set("response_type", "token") }, "unsupported_response_type"},
		"no PKCE":        {func(q url.Values) { q.Del("code_challenge") }, "invalid_request"},
		"plain PKCE":     {func(q url.Values) { q.Set("code_challenge_method", "plain") }, "invalid_request"},
		"unknown scope":  {func(q url.Values) { q.Set("scope", "articles:read admin") }, "invalid_scope"},
		"other resource": {func(q url.Values) { q.Set("resource", "https://other.example.com/mcp") }, "invalid_target"},
	} {
		res := e.browser.Get(oauthserver.AuthorizePath + "?" + authorizeQuery(tc.edit).Encode())
		u, _ := url.Parse(res.Redirect())
		if res.Code != http.StatusFound || u == nil || u.Query().Get("error") != tc.want || u.Query().Get("state") != "xyz" || u.Query().Get("iss") != issuer {
			t.Errorf("%s: %d %q", name, res.Code, res.Redirect())
		}
	}

	// 用户拒绝。
	back := e.approve(t, authorizeQuery(nil), "deny")
	if back.Query().Get("error") != "access_denied" || back.Query().Get("code") != "" {
		t.Fatalf("deny: %s", back)
	}

	// 客户端元数据中的 client_id 与地址不一致时不认这个客户端。
	e.fake.On(cimdPattern).Reply(outboundtest.JSON(200, map[string]any{
		"client_id": "https://client.example.com/other.json", "client_name": "Claude", "redirect_uris": []string{callback},
	}))
	if res := e.browser.Get(oauthserver.AuthorizePath + "?" + authorizeQuery(nil).Encode()); res.Code != http.StatusUnprocessableEntity {
		t.Fatalf("mismatched metadata: %d", res.Code)
	}
}

func TestPreRegisteredLoopbackClient(t *testing.T) {
	e := setup(t)
	e.browser.Login(e.pages, editor)
	// 本机回调地址忽略端口；不传 scope 时申请全部范围，不传 resource 时用唯一的资源。
	redirect := "http://localhost:3118/callback"
	q := authorizeQuery(func(q url.Values) {
		q.Set("client_id", "agent")
		q.Set("redirect_uri", redirect)
		q.Del("scope")
		q.Del("resource")
	})
	res := e.browser.Get(oauthserver.AuthorizePath + "?" + q.Encode())
	if res.Code != http.StatusOK || !strings.Contains(res.Text(), "内部 agent") || !strings.Contains(res.Text(), "修改文章") {
		t.Fatalf("consent: %d %s", res.Code, res.Text())
	}
	back := e.approve(t, q, "allow")
	form := tokenForm(back.Query().Get("code"))
	form.Set("client_id", "agent")
	form.Set("redirect_uri", redirect)
	form.Del("resource")
	status, body := e.exchange(t, form)
	if status != http.StatusOK || body["scope"] != "articles:read articles:write" {
		t.Fatalf("token: %d %v", status, body)
	}
	// 没有取回任何客户端元数据。
	if n := len(e.fake.Sent(cimdPattern)); n != 0 {
		t.Fatalf("fetched metadata %d times", n)
	}
}

func TestNewPanics(t *testing.T) {
	db := dbtest.Open(t, migrations(t))
	tokens := apitoken.NewStore(db, apitoken.Config{Lifetime: time.Hour})
	ok := oauthserver.Options{Issuer: issuer, Resources: []string{resource}, Scopes: []oauthserver.Scope{{Name: "articles:read"}}}
	for name, edit := range map[string]func(*oauthserver.Options){
		"issuer path":  func(o *oauthserver.Options) { o.Issuer = issuer + "/auth" },
		"http issuer":  func(o *oauthserver.Options) { o.Issuer = "http://example.com" },
		"no resources": func(o *oauthserver.Options) { o.Resources = nil },
		"bad resource": func(o *oauthserver.Options) { o.Resources = []string{"ftp://example.com/mcp"} },
		"no scopes":    func(o *oauthserver.Options) { o.Scopes = nil },
		"bad scope":    func(o *oauthserver.Options) { o.Scopes = []oauthserver.Scope{{Name: "a b"}} },
		"url client id": func(o *oauthserver.Options) {
			o.Clients = []oauthserver.Client{{ID: cimd, RedirectURIs: []string{callback}}}
		},
		"no redirects": func(o *oauthserver.Options) { o.Clients = []oauthserver.Client{{ID: "agent"}} },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			opts := ok
			edit(&opts)
			oauthserver.New(db, tokens, opts)
		}()
	}
	oauthserver.New(db, tokens, ok)
}

// 客户端换一种认证方式重试时，同一个授权码仍然可以使用，例如 golang.org/x/oauth2 先用 Basic 认证、失败后改放表单。
func TestTokenRetry(t *testing.T) {
	e := setup(t)
	e.browser.Login(e.pages, editor)
	c := e.approve(t, authorizeQuery(nil), "allow").Query().Get("code")

	wrong := tokenForm(c)
	wrong.Set("client_id", "agent")
	if status, body := e.exchange(t, wrong); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("wrong client: %d %v", status, body)
	}

	// Basic 认证中的 client_id 按表单编码（RFC 6749 2.3.1）。
	form := tokenForm(c)
	form.Del("client_id")
	req := httptest.NewRequest(http.MethodPost, issuer+oauthserver.TokenPath, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(cimd), "")
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "access_token") {
		t.Fatalf("basic auth: %d %s", w.Code, w.Body)
	}
	// 兑换成功后不能再用。
	if status, body := e.exchange(t, tokenForm(c)); status != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("reused: %d %v", status, body)
	}
}
