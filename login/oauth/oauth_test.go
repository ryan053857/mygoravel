package oauth_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"

	"github.com/ryan053857/mygoravel/login"
	"github.com/ryan053857/mygoravel/login/oauth"
)

const (
	clientID     = "blog-web"
	iosClientID  = "blog-ios"
	clientSecret = "secret"
	redirectURL  = "https://blog.example.com/auth/google/callback"
	appRedirect  = "https://app.example.com/auth/callback"
)

// idp 是一个最小的 OpenID Connect 提供方：发现文档、授权、换取 token 和公钥。
type idp struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu     sync.Mutex
	grants map[string]grant // 授权码 → 授权请求
	claims map[string]any   // 签发的 ID token 中额外的声明，也是 /user 返回的账号
}

type grant struct{ challenge, nonce, redirect string }

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, grants: map[string]grant{}, claims: map[string]any{"email": "alice@example.com", "email_verified": true, "name": "Alice"}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{
			"issuer":                                p.URL,
			"authorization_endpoint":                p.URL + "/authorize",
			"token_endpoint":                        p.URL + "/token",
			"jwks_uri":                              p.URL + "/keys",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /keys", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "k1", Algorithm: "RS256", Use: "sig"}}})
	})
	// authorize 模拟用户同意授权：记下 PKCE challenge 和 nonce，带着授权码跳回 redirect_uri。
	mux.HandleFunc("GET /authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != clientID && q.Get("client_id") != iosClientID {
			http.Error(w, "bad client", http.StatusBadRequest)
			return
		}
		code := rand.Text()
		p.mu.Lock()
		p.grants[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	// token 校验客户端密钥、redirect_uri 和 PKCE verifier（发起时带了 challenge 的话），授权码只能用一次。
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		id, secret, ok := r.BasicAuth()
		if !ok {
			id, secret = r.PostFormValue("client_id"), r.PostFormValue("client_secret")
		}
		p.mu.Lock()
		g, found := p.grants[r.PostFormValue("code")]
		delete(p.grants, r.PostFormValue("code"))
		p.mu.Unlock()
		sum := sha256.Sum256([]byte(r.PostFormValue("code_verifier")))
		pkce := g.challenge == "" || base64.RawURLEncoding.EncodeToString(sum[:]) == g.challenge
		if id != clientID || secret != clientSecret || !found || g.redirect != r.PostFormValue("redirect_uri") || !pkce {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		writeJSON(w, map[string]any{
			"access_token": rand.Text(), "token_type": "Bearer", "expires_in": 3600,
			"id_token": p.idToken(t, clientID, g.nonce, time.Now().Add(time.Hour)),
		})
	})
	// user 是只支持 OAuth 2.0 的提供方读取账号的接口（类似 GitHub 的 /user），要求带上 access token。
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"id": 42, "login": "alice", "email": p.claims["email"]})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// idToken 签发一个 ID token。
func (p *idp) idToken(t *testing.T, audience, nonce string, expiry time.Time) string {
	t.Helper()
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: p.key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	claims := map[string]any{"iss": p.URL, "sub": "google-42", "aud": audience, "iat": time.Now().Unix(), "exp": expiry.Unix()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	maps.Copy(claims, p.claims)
	raw, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// authorize 模拟浏览器打开授权地址并被带回 redirect_uri，返回回调的查询参数。
func authorize(t *testing.T, authURL string) url.Values {
	t.Helper()
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || resp.StatusCode != http.StatusFound {
		t.Fatalf("authorize: %d %v", resp.StatusCode, err)
	}
	return loc.Query()
}

func newProvider(t *testing.T, p *idp, edit ...func(*oauth.Config)) *oauth.Provider {
	t.Helper()
	cfg := oauth.Config{
		Provider: "google", Issuer: p.URL, ClientID: clientID, ClientSecret: clientSecret,
		RedirectURL: redirectURL, ClientRedirectURLs: []string{appRedirect},
	}
	for _, f := range edit {
		f(&cfg)
	}
	provider, err := oauth.Discover(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

var alice = login.External{Provider: "google", Subject: "google-42", Email: "alice@example.com", EmailVerified: true, Name: "Alice"}

func is(t *testing.T, err, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want %v", err, want)
	}
}

func TestPageFlow(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	provider := newProvider(t, p)

	authURL, flow := provider.Start()
	u, _ := url.Parse(authURL)
	if q := u.Query(); q.Get("state") != flow.State || q.Get("nonce") != flow.Nonce || q.Get("code_challenge_method") != "S256" ||
		q.Get("redirect_uri") != redirectURL || q.Get("scope") != "openid email profile" || flow.Verifier == "" {
		t.Fatalf("auth url %s, flow %+v", authURL, flow)
	}
	// Flow 可以编码成一个字符串保存。
	restored, err := oauth.ParseFlow(flow.String())
	if err != nil || restored != flow {
		t.Fatalf("parse flow: %+v %v", restored, err)
	}
	params := authorize(t, authURL)
	got, err := provider.Finish(ctx, restored, params)
	if err != nil || got != alice {
		t.Fatalf("finish: %+v %v", got, err)
	}
	// 授权码只能用一次。
	_, err = provider.Finish(ctx, flow, params)
	is(t, err, oauth.ErrInvalid)

	// state 不符：不是本站发起的登录。
	authURL, flow = provider.Start()
	params = authorize(t, authURL)
	params.Set("state", "forged")
	_, err = provider.Finish(ctx, flow, params)
	is(t, err, oauth.ErrInvalid)
	_, err = provider.Finish(ctx, oauth.Flow{}, url.Values{})
	is(t, err, oauth.ErrInvalid)

	// 用户取消授权。
	_, flow = provider.Start()
	_, err = provider.Finish(ctx, flow, url.Values{"state": {flow.State}, "error": {"access_denied"}})
	is(t, err, oauth.ErrDenied)

	// PKCE verifier 不对时换不到 token。
	authURL, flow = provider.Start()
	params = authorize(t, authURL)
	flow.Verifier = "wrong-verifier-wrong-verifier-wrong-verifier-wrong"
	_, err = provider.Finish(ctx, flow, params)
	is(t, err, oauth.ErrInvalid)

	for _, bad := range []string{"", "a.b", "a..c", "..c"} {
		if _, err := oauth.ParseFlow(bad); !errors.Is(err, oauth.ErrInvalid) {
			t.Errorf("ParseFlow(%q): %v", bad, err)
		}
	}
}

// 单页应用和 App 自己完成跳转，把授权码交给 API。
func TestExchange(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	provider := newProvider(t, p)
	client := func(redirect string) (oauth.Grant, url.Values) {
		verifier, nonce := "client-verifier-0123456789-0123456789-0123456789", rand.Text()
		sum := sha256.Sum256([]byte(verifier))
		q := url.Values{
			"client_id": {clientID}, "redirect_uri": {redirect}, "state": {"s"}, "nonce": {nonce},
			"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"},
		}
		params := authorize(t, p.URL+"/authorize?"+q.Encode())
		return oauth.Grant{Code: params.Get("code"), Verifier: verifier, RedirectURI: redirect, Nonce: nonce}, params
	}

	g, _ := client(appRedirect)
	if got, err := provider.Exchange(ctx, g); err != nil || got != alice {
		t.Fatalf("exchange: %+v %v", got, err)
	}
	// 回调地址不在登记的列表中：不去换取 token。
	g, _ = client("https://evil.example.com/cb")
	_, err := provider.Exchange(ctx, g)
	is(t, err, oauth.ErrInvalid)
	// 缺少 verifier、nonce 不符。
	g, _ = client(appRedirect)
	noVerifier := g
	noVerifier.Verifier = ""
	_, err = provider.Exchange(ctx, noVerifier)
	is(t, err, oauth.ErrInvalid)
	g.Nonce = "other"
	_, err = provider.Exchange(ctx, g)
	is(t, err, oauth.ErrInvalid)
}

// App 原生登录：SDK 返回的 ID token 的受众是 App 的 client ID。
func TestIDToken(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	provider := newProvider(t, p, func(c *oauth.Config) { c.Audiences = []string{clientID, iosClientID} })

	raw := p.idToken(t, iosClientID, "n1", time.Now().Add(time.Hour))
	if got, err := provider.IDToken(ctx, raw, "n1"); err != nil || got != alice {
		t.Fatalf("ios token: %+v %v", got, err)
	}
	if _, err := provider.IDToken(ctx, raw, ""); err != nil {
		t.Fatalf("without nonce check: %v", err)
	}
	for name, token := range map[string]string{
		"wrong nonce":    raw,
		"other audience": p.idToken(t, "someone-else", "n1", time.Now().Add(time.Hour)),
		"expired":        p.idToken(t, iosClientID, "n1", time.Now().Add(-time.Minute)),
		"tampered":       raw[:len(raw)-4] + "AAAA",
		"empty":          "",
	} {
		nonce := "n1"
		if name == "wrong nonce" {
			nonce = "n2"
		}
		if _, err := provider.IDToken(ctx, token, nonce); !errors.Is(err, oauth.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// 默认只接受 ClientID 作为受众。
	if _, err := newProvider(t, p).IDToken(ctx, raw, "n1"); !errors.Is(err, oauth.ErrInvalid) {
		t.Fatalf("ios audience accepted by default: %v", err)
	}
	// 另一个签发者的 token。
	other := newIDP(t)
	if _, err := provider.IDToken(ctx, other.idToken(t, iosClientID, "n1", time.Now().Add(time.Hour)), "n1"); !errors.Is(err, oauth.ErrInvalid) {
		t.Fatalf("token from another issuer accepted: %v", err)
	}
}

// Apple：email_verified 是字符串，不使用 PKCE，回调是表单 POST。
func TestAppleQuirks(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	p.claims = map[string]any{"email": "x@privaterelay.appleid.com", "email_verified": "true"}
	provider := newProvider(t, p, func(c *oauth.Config) { c.Provider, c.NoPKCE, c.FormPost = "apple", true, true })

	authURL, flow := provider.Start()
	u, _ := url.Parse(authURL)
	if q := u.Query(); q.Has("code_challenge") || q.Get("response_mode") != "form_post" || flow.Verifier != "" {
		t.Fatalf("auth url %s", authURL)
	}
	restored, err := oauth.ParseFlow(flow.String())
	if err != nil {
		t.Fatal(err)
	}
	got, err := provider.Finish(ctx, restored, authorize(t, authURL))
	if err != nil || got.Provider != "apple" || !got.EmailVerified || got.Name != "" {
		t.Fatalf("apple: %+v %v", got, err)
	}
	p.claims["email_verified"] = "false"
	if got, _ := provider.IDToken(ctx, p.idToken(t, clientID, "", time.Now().Add(time.Hour)), ""); got.EmailVerified {
		t.Fatal(`"false" read as verified`)
	}
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	for name, cfg := range map[string]oauth.Config{
		"bad provider":    {Provider: "Google", Issuer: p.URL, ClientID: clientID},
		"email provider":  {Provider: "email", Issuer: p.URL, ClientID: clientID},
		"no client":       {Provider: "google", Issuer: p.URL},
		"no issuer":       {Provider: "google", ClientID: clientID},
		"issuer mismatch": {Provider: "google", Issuer: p.URL + "/", ClientID: clientID},
		"unreachable":     {Provider: "google", Issuer: "http://127.0.0.1:1", ClientID: clientID},
	} {
		if _, err := oauth.Discover(ctx, cfg); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// 只支持 OAuth 2.0 的提供方（GitHub、微信等）：授权码流程相同，账号由应用的函数读取。
func TestPlainOAuth2(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	endpoint := oauth2.Endpoint{AuthURL: p.URL + "/authorize", TokenURL: p.URL + "/token"}
	account := func(ctx context.Context, client *http.Client, tok *oauth2.Token) (login.External, error) {
		resp, err := client.Get(p.URL + "/user")
		if err != nil {
			return login.External{}, err
		}
		defer resp.Body.Close()
		var u struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
			return login.External{}, err
		}
		return login.External{Provider: "ignored", Subject: fmt.Sprint(u.ID), Name: u.Login}, nil
	}
	github, err := oauth.New(oauth.Config{
		Provider: "github", ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
		ClientRedirectURLs: []string{appRedirect}, Scopes: []string{"read:user"},
	}, endpoint, account)
	if err != nil {
		t.Fatal(err)
	}
	authURL, flow := github.Start()
	if u, _ := url.Parse(authURL); u.Query().Has("nonce") || u.Query().Get("scope") != "read:user" {
		t.Fatalf("auth url %s", authURL)
	}
	got, err := github.Finish(ctx, flow, authorize(t, authURL))
	if err != nil || got != (login.External{Provider: "github", Subject: "42", Name: "alice"}) {
		t.Fatalf("finish: %+v %v", got, err)
	}
	// 客户端自己完成授权时不需要 nonce。
	verifier := oauth2.GenerateVerifier()
	q := url.Values{"client_id": {clientID}, "redirect_uri": {appRedirect}, "state": {"s"},
		"code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "code_challenge_method": {"S256"}}
	params := authorize(t, p.URL+"/authorize?"+q.Encode())
	if got, err := github.Exchange(ctx, oauth.Grant{Code: params.Get("code"), Verifier: verifier, RedirectURI: appRedirect}); err != nil || got.Subject != "42" {
		t.Fatalf("exchange: %+v %v", got, err)
	}
	if _, err := github.IDToken(ctx, "x", ""); err == nil {
		t.Fatal("IDToken accepted by an OAuth 2.0 provider")
	}
	for name, build := range map[string]func() (*oauth.Provider, error){
		"no endpoint": func() (*oauth.Provider, error) {
			return oauth.New(oauth.Config{Provider: "github", ClientID: clientID}, oauth2.Endpoint{}, account)
		},
		"no account": func() (*oauth.Provider, error) {
			return oauth.New(oauth.Config{Provider: "github", ClientID: clientID}, endpoint, nil)
		},
	} {
		if _, err := build(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
