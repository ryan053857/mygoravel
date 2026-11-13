package oauthserver_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/apitoken"
	"github.com/ryan053857/mygoravel/database/dbtest"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/mcpx"
	"github.com/ryan053857/mygoravel/oauthserver"
	"github.com/ryan053857/mygoravel/outbound/outboundtest"
)

const (
	issuer      = "https://blog.example.com"
	resource    = issuer + "/mcp"
	consentPage = "/oauth/consent"
	consentAPI  = "/api" + oauthserver.ConsentPath
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

type env struct {
	db     *bun.DB
	tokens *apitoken.Store
	fake   *outboundtest.Transport
	router chi.Router
	srv    *oauthserver.Server
	// login 是 editor 在单页应用中登录得到的 token。
	login string
	// base 是 Issuer 的路径，Routes 和 API 挂在它下面；resource、consentPage 是这次的 MCP 地址和同意页路径。
	base, resource, consentPage string
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
	editors := func(_ context.Context, sub identity.Subject) (identity.Subject, error) {
		sub.Permissions = []string{"articles:*"}
		return sub, nil
	}
	opts := oauthserver.Options{
		Issuer:     issuer,
		Resources:  []string{resource},
		Scopes:     []oauthserver.Scope{{Name: "articles:read", Description: "查看文章"}, {Name: "articles:write", Description: "修改文章"}},
		Clients:    []oauthserver.Client{{ID: "agent", Name: "内部 agent", RedirectURIs: []string{"http://localhost/callback"}}},
		ConsentURL: consentPage,
		HTTPClient: fake.Client(),
		Logger:     slog.New(slog.DiscardHandler),
	}
	if edit != nil {
		edit(&opts)
	}
	srv := oauthserver.New(db, tokens, opts)
	issuerURL, _ := url.Parse(opts.Issuer)
	base := issuerURL.Path
	router := chi.NewRouter()
	router.Handle(srv.MetadataPath(), srv.Metadata())

	apiRouter := chi.NewRouter()
	api := rest.New(rest.Options{Router: apiRouter, BasePath: base + "/api", Accounts: identity.Accounts{"editor": editors}, Logger: slog.New(slog.DiscardHandler)})
	srv.Register(api, rest.RequireBearer(api, tokens.Validate))
	router.Route(base+"/", func(r chi.Router) {
		srv.Routes(r)
		r.Mount("/api", apiRouter)
	})

	tools := mcpx.New(mcpx.Options{Name: "blog"})
	mcpx.Register(tools, mcpx.Tool{Name: "whoami", Description: "当前主体", Permissions: []string{"articles:read"}, ReadOnly: true},
		func(ctx context.Context, _ struct{}) (map[string]any, error) {
			sub, _ := identity.FromContext(ctx)
			return map[string]any{"id": sub.ID, "scopes": sub.Scopes}, nil
		})
	res := srv.Resource(opts.Resources[0])
	router.Handle(res.MetadataPath(), mcpx.ResourceMetadata(res))
	resourceURL, _ := url.Parse(opts.Resources[0])
	router.Handle(resourceURL.Path, mcpx.Handler(tools, mcpx.HandlerOptions{
		Validate: tokens.ValidateFor(res.URL), Accounts: identity.Accounts{"editor": editors}, Resource: &res,
	}))
	login, err := tokens.Login(context.Background(), editor, apitoken.Device{})
	if err != nil {
		t.Fatal(err)
	}
	consentURL, _ := url.Parse(opts.ConsentURL)
	return &env{db: db, tokens: tokens, fake: fake, router: router, srv: srv, login: login,
		base: base, resource: opts.Resources[0], consentPage: consentURL.Path}
}

// do 发出请求；token 不为空时带上 Bearer，body 不为 nil 时编码为 JSON。
func (e *env) do(method, target, token string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, issuer+target, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req)
	return w
}

// authorize 访问授权端点，返回它跳转去的地址。
func (e *env) authorize(t *testing.T, q url.Values) *url.URL {
	t.Helper()
	w := e.do(http.MethodGet, e.base+oauthserver.AuthorizePath+"?"+q.Encode(), "", nil)
	u, err := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusFound || err != nil {
		t.Fatalf("authorize: %d %q", w.Code, w.Header().Get("Location"))
	}
	return u
}

// request 访问授权端点，返回同意页地址中的 request 参数。
func (e *env) request(t *testing.T, q url.Values) string {
	t.Helper()
	u := e.authorize(t, q)
	if u.Path != e.consentPage || u.Query().Get("request") == "" {
		t.Fatalf("not sent to the consent page: %s", u)
	}
	return u.Query().Get("request")
}

// consent 以 token 读取同意页要显示的授权请求。
func (e *env) consent(token, request string) (int, map[string]any) {
	w := e.do(http.MethodGet, e.base+consentAPI+"?"+url.Values{"request": {request}}.Encode(), token, nil)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// decide 以 token 对 request 做出选择，返回状态码和响应体。
func (e *env) decide(token, request string, approve bool) (int, map[string]any) {
	w := e.do(http.MethodPost, e.base+consentAPI, token, map[string]any{"request": request, "approve": approve})
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return w.Code, body
}

// approve 以 editor 的登录 token 对 q 的授权做出选择，返回跳回客户端的地址。
func (e *env) approve(t *testing.T, q url.Values, approve bool) *url.URL {
	t.Helper()
	status, body := e.decide(e.login, e.request(t, q), approve)
	raw, _ := body["url"].(string)
	u, err := url.Parse(raw)
	if status != http.StatusOK || raw == "" || err != nil {
		t.Fatalf("decide: %d %v", status, body)
	}
	return u
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

func (e *env) exchange(t *testing.T, form url.Values) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, issuer+e.base+oauthserver.TokenPath, strings.NewReader(form.Encode()))
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
	t.Parallel()
	e := setup(t)

	// 客户端先访问 MCP：401 告诉它受保护资源元数据的地址，元数据指向授权服务器。
	code, challengeHeader, _ := e.callTool(t, "")
	if code != http.StatusUnauthorized || challengeHeader != `Bearer resource_metadata="`+issuer+`/.well-known/oauth-protected-resource/mcp"` {
		t.Fatalf("unauthenticated: %d %q", code, challengeHeader)
	}
	prm := e.do(http.MethodGet, "/.well-known/oauth-protected-resource/mcp", "", nil)
	var meta map[string]any
	if err := json.Unmarshal(prm.Body.Bytes(), &meta); err != nil || meta["resource"] != resource ||
		fmt.Sprint(meta["authorization_servers"]) != "["+issuer+"]" || fmt.Sprint(meta["scopes_supported"]) != "[articles:read articles:write]" {
		t.Fatalf("resource metadata %s", prm.Body)
	}
	if e.srv.MetadataPath() != "/.well-known/oauth-authorization-server" {
		t.Fatalf("metadata path %q", e.srv.MetadataPath())
	}
	as := e.do(http.MethodGet, e.srv.MetadataPath(), "", nil)
	if err := json.Unmarshal(as.Body.Bytes(), &meta); err != nil || meta["issuer"] != issuer ||
		meta["authorization_endpoint"] != issuer+"/oauth/authorize" ||
		meta["token_endpoint"] != issuer+"/oauth/token" || meta["client_id_metadata_document_supported"] != true ||
		fmt.Sprint(meta["token_endpoint_auth_methods_supported"]) != "[none]" || fmt.Sprint(meta["code_challenge_methods_supported"]) != "[S256]" {
		t.Fatalf("server metadata %s", as.Body)
	}

	// 授权端点校验请求后跳到单页应用的同意页，附上 request。
	q := authorizeQuery(nil)
	request := e.request(t, q)

	// 同意页用登录 token 读取请求：应用、范围、跳转去向和资源。没有登录时 401，由单页应用先去登录。
	if status, _ := e.consent("", request); status != http.StatusUnauthorized {
		t.Fatalf("not logged in: %d", status)
	}
	status, c := e.consent(e.login, request)
	if status != http.StatusOK || c["client_id"] != cimd || c["client_name"] != "Claude" || c["redirect_host"] != "client.example.com" ||
		c["resource"] != resource || fmt.Sprint(c["scopes"]) != "[map[description:查看文章 name:articles:read]]" {
		t.Fatalf("consent: %d %v", status, c)
	}

	// 个人访问令牌有权限限制，不代表用户本人，不能用来授权。
	personal, err := e.tokens.Create(context.Background(), editor, apitoken.Personal{Name: "script", Scopes: []string{"*"}})
	if err != nil {
		t.Fatal(err)
	}
	if status, body := e.consent(personal, request); status != http.StatusForbidden || body["code"] != "oauth.restricted_credential" {
		t.Fatalf("personal token consent: %d %v", status, body)
	}
	if status, body := e.decide(personal, request, true); status != http.StatusForbidden || body["code"] != "oauth.restricted_credential" {
		t.Fatalf("personal token decide: %d %v", status, body)
	}

	// 同意：带着授权码、state 和 iss 跳回客户端。
	back := e.approve(t, q, true)
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
	// 用户在“已连接的应用”中看到它，与登录 token、个人访问令牌并列。
	list, err := e.tokens.List(context.Background(), editor)
	if err != nil || len(list) != 3 || !slices.ContainsFunc(list, func(tk apitoken.Token) bool { return tk.Kind == apitoken.KindOAuth && tk.ClientName == "Claude" }) {
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
	t.Parallel()
	e := setup(t)
	issue := func() string {
		return e.approve(t, authorizeQuery(nil), true).Query().Get("code")
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
	t.Parallel()
	e := setup(t)

	// 不能确认回调地址属于这个客户端：不跳回，把错误码交给同意页。
	for name, tc := range map[string]struct {
		edit func(url.Values)
		want string
	}{
		"unknown client":    {func(q url.Values) { q.Set("client_id", "nobody") }, "oauth.invalid_client"},
		"http metadata URL": {func(q url.Values) { q.Set("client_id", "http://client.example.com/oauth/client.json") }, "oauth.invalid_client"},
		"foreign redirect":  {func(q url.Values) { q.Set("redirect_uri", "https://evil.example.com/callback") }, "oauth.invalid_redirect_uri"},
		"no redirect":       {func(q url.Values) { q.Del("redirect_uri") }, "oauth.invalid_redirect_uri"},
	} {
		u := e.authorize(t, authorizeQuery(tc.edit))
		if u.Path != consentPage || u.Query().Get("error") != tc.want || u.Query().Get("request") != "" {
			t.Errorf("%s: %s", name, u)
		}
		// 改过的 request 交给同意页的接口同样被拒绝。
		if status, body := e.consent(e.login, authorizeQuery(tc.edit).Encode()); status != http.StatusUnprocessableEntity || body["code"] != tc.want {
			t.Errorf("%s: consent %d %v", name, status, body)
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
		u := e.authorize(t, authorizeQuery(tc.edit))
		if u.Scheme+"://"+u.Host+u.Path != callback || u.Query().Get("error") != tc.want || u.Query().Get("state") != "xyz" || u.Query().Get("iss") != issuer {
			t.Errorf("%s: %s", name, u)
		}
		if status, body := e.decide(e.login, authorizeQuery(tc.edit).Encode(), true); status != http.StatusUnprocessableEntity || body["code"] != "oauth.invalid_request" {
			t.Errorf("%s: decide %d %v", name, status, body)
		}
	}

	// 用户拒绝。
	back := e.approve(t, authorizeQuery(nil), false)
	if back.Query().Get("error") != "access_denied" || back.Query().Get("code") != "" {
		t.Fatalf("deny: %s", back)
	}

	// 客户端元数据中的 client_id 与地址不一致时不认这个客户端。
	e.fake.On(cimdPattern).Reply(outboundtest.JSON(200, map[string]any{
		"client_id": "https://client.example.com/other.json", "client_name": "Claude", "redirect_uris": []string{callback},
	}))
	if u := e.authorize(t, authorizeQuery(nil)); u.Query().Get("error") != "oauth.invalid_client" {
		t.Fatalf("mismatched metadata: %s", u)
	}
}

func TestPreRegisteredLoopbackClient(t *testing.T) {
	t.Parallel()
	e := setup(t)
	// 本机回调地址忽略端口；不传 scope 时申请全部范围，不传 resource 时用唯一的资源。
	redirect := "http://localhost:3118/callback"
	q := authorizeQuery(func(q url.Values) {
		q.Set("client_id", "agent")
		q.Set("redirect_uri", redirect)
		q.Del("scope")
		q.Del("resource")
	})
	status, c := e.consent(e.login, e.request(t, q))
	if status != http.StatusOK || c["client_name"] != "内部 agent" || c["resource"] != resource || !strings.Contains(fmt.Sprint(c["scopes"]), "修改文章") {
		t.Fatalf("consent: %d %v", status, c)
	}
	back := e.approve(t, q, true)
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
	t.Parallel()
	db := dbtest.Open(t, migrations(t))
	tokens := apitoken.NewStore(db, apitoken.Config{Lifetime: time.Hour})
	ok := oauthserver.Options{Issuer: issuer, Resources: []string{resource}, Scopes: []oauthserver.Scope{{Name: "articles:read"}}, ConsentURL: consentPage}
	for name, edit := range map[string]func(*oauthserver.Options){
		"issuer query": func(o *oauthserver.Options) { o.Issuer = issuer + "?a=1" },
		"http issuer":  func(o *oauthserver.Options) { o.Issuer = "http://example.com" },
		"no resources": func(o *oauthserver.Options) { o.Resources = nil },
		"bad resource": func(o *oauthserver.Options) { o.Resources = []string{"ftp://example.com/mcp"} },
		"no scopes":    func(o *oauthserver.Options) { o.Scopes = nil },
		"bad scope":    func(o *oauthserver.Options) { o.Scopes = []oauthserver.Scope{{Name: "a b"}} },
		"url client id": func(o *oauthserver.Options) {
			o.Clients = []oauthserver.Client{{ID: cimd, RedirectURIs: []string{callback}}}
		},
		"no redirects":          func(o *oauthserver.Options) { o.Clients = []oauthserver.Client{{ID: "agent"}} },
		"no consent":            func(o *oauthserver.Options) { o.ConsentURL = "" },
		"relative consent":      func(o *oauthserver.Options) { o.ConsentURL = "oauth/consent" },
		"protocol-relative URL": func(o *oauthserver.Options) { o.ConsentURL = "//evil.example.com/consent" },
		"http consent":          func(o *oauthserver.Options) { o.ConsentURL = "http://app.example.com/consent" },
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
	t.Parallel()
	e := setup(t)
	c := e.approve(t, authorizeQuery(nil), true).Query().Get("code")

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

// 同意页可以是另一个地址上的单页应用，地址中原有的参数保留。
func TestConsentURLWithQuery(t *testing.T) {
	t.Parallel()
	e := setupWith(t, func(o *oauthserver.Options) { o.ConsentURL = "https://app.example.com/consent?lang=zh" })
	u := e.authorize(t, authorizeQuery(nil))
	if u.Scheme+"://"+u.Host+u.Path != "https://app.example.com/consent" || u.Query().Get("lang") != "zh" || u.Query().Get("request") == "" {
		t.Fatalf("consent URL %s", u)
	}
}

// 应用部署在路径之下：端点在 Issuer 的路径上，元数据仍在域名的根上，路径插在 .well-known 之后（RFC 8414）。
func TestIssuerWithPath(t *testing.T) {
	t.Parallel()
	e := setupWith(t, withPath)
	if e.srv.MetadataPath() != "/.well-known/oauth-authorization-server/blog" {
		t.Fatalf("metadata path %q", e.srv.MetadataPath())
	}
	var meta map[string]any
	if err := json.Unmarshal(e.do(http.MethodGet, e.srv.MetadataPath(), "", nil).Body.Bytes(), &meta); err != nil ||
		meta["issuer"] != issuer+"/blog" || meta["authorization_endpoint"] != issuer+"/blog/oauth/authorize" ||
		meta["token_endpoint"] != issuer+"/blog/oauth/token" || meta["revocation_endpoint"] != issuer+"/blog/oauth/revoke" {
		t.Fatalf("metadata %v %v", meta, err)
	}
	q := authorizeQuery(func(q url.Values) { q.Set("resource", e.resource) })
	back := e.approve(t, q, true)
	if back.Query().Get("iss") != issuer+"/blog" {
		t.Fatalf("iss %s", back)
	}
	form := tokenForm(back.Query().Get("code"))
	form.Set("resource", e.resource)
	if status, body := e.exchange(t, form); status != http.StatusOK || body["access_token"] == "" {
		t.Fatalf("token: %d %v", status, body)
	}
}

func withPath(o *oauthserver.Options) {
	o.Issuer = issuer + "/blog"
	o.Resources = []string{issuer + "/blog/mcp"}
	o.ConsentURL = "/blog/oauth/consent"
}
