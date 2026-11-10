package doctest_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

// 以下行为与 docs/social-login.md 一致：用 x/oauth2 走带 PKCE 的授权码流程，
// 用 go-oidc 校验 id_token，nonce 由调用方比较。

const (
	clientID     = "blog"
	clientSecret = "secret"
	redirectURL  = "https://blog.example.com/auth/google/callback"
)

// idp 是一个最小的 OpenID Connect 提供方：发现文档、授权、换取 token 和公钥。
type idp struct {
	*httptest.Server
	key *rsa.PrivateKey

	mu     sync.Mutex
	grants map[string]grant // 授权码 → 授权请求
}

type grant struct{ challenge, nonce, redirect string }

func newIDP(t *testing.T) *idp {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := &idp{key: key, grants: map[string]grant{}}
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
		if q.Get("client_id") != clientID || q.Get("code_challenge_method") != "S256" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		code := rand.Text()
		p.mu.Lock()
		p.grants[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce"), redirect: q.Get("redirect_uri")}
		p.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code="+code+"&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	// token 校验客户端密钥、redirect_uri 和 PKCE verifier，授权码只能用一次。
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
		if id != clientID || secret != clientSecret || !found || g.redirect != r.PostFormValue("redirect_uri") ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		writeJSON(w, map[string]any{
			"access_token": rand.Text(),
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     p.idToken(t, p.key, clientID, g.nonce, time.Now().Add(time.Hour)),
		})
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// idToken 用 key 签发一个 id_token。
func (p *idp) idToken(t *testing.T, key *rsa.PrivateKey, audience, nonce string, expiry time.Time) string {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "k1"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := jwt.Signed(signer).Claims(map[string]any{
		"iss":            p.URL,
		"sub":            "google-42",
		"aud":            audience,
		"iat":            time.Now().Unix(),
		"exp":            expiry.Unix(),
		"nonce":          nonce,
		"email":          "alice@example.com",
		"email_verified": true,
	}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// authorize 模拟浏览器打开授权地址并被带回 redirect_uri，返回回调中的授权码和 state。
func authorize(t *testing.T, authURL string) (code, state string) {
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
	return loc.Query().Get("code"), loc.Query().Get("state")
}

func TestOAuth2PKCE(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     oauth2.Endpoint{AuthURL: p.URL + "/authorize", TokenURL: p.URL + "/token"},
		RedirectURL:  redirectURL,
		Scopes:       []string{"openid", "email"},
	}

	state, verifier := rand.Text(), oauth2.GenerateVerifier()
	authURL := conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier))
	if !strings.Contains(authURL, "code_challenge_method=S256") || strings.Contains(authURL, verifier) {
		t.Fatalf("auth URL must carry the S256 challenge, not the verifier: %s", authURL)
	}
	code, gotState := authorize(t, authURL)
	if gotState != state {
		t.Fatalf("state=%q want %q", gotState, state)
	}

	// 换取 token 时必须带上同一个 verifier。
	wrong := oauth2.GenerateVerifier()
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(wrong)); err == nil {
		t.Fatal("exchange with a different verifier succeeded")
	} else if re := (*oauth2.RetrieveError)(nil); !errors.As(err, &re) || re.ErrorCode != "invalid_grant" {
		t.Fatalf("err=%v", err)
	}
	code, _ = authorize(t, authURL)
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil || tok.AccessToken == "" {
		t.Fatalf("exchange: %v", err)
	}
	if _, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier)); err == nil {
		t.Fatal("authorization code reused")
	}
}

func TestOIDCIDToken(t *testing.T) {
	ctx := context.Background()
	p := newIDP(t)
	provider, err := oidc.NewProvider(ctx, p.URL)
	if err != nil {
		t.Fatal(err)
	}
	conf := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  redirectURL,
		Scopes:       []string{oidc.ScopeOpenID, "email"},
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: clientID})

	state, nonce, pkce := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	code, _ := authorize(t, conf.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(pkce)))
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(pkce))
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		t.Fatal("token response has no id_token")
	}
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	var claims struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := idToken.Claims(&claims); err != nil {
		t.Fatal(err)
	}
	if idToken.Subject != "google-42" || idToken.Nonce != nonce || claims.Email != "alice@example.com" || !claims.EmailVerified {
		t.Fatalf("token=%+v claims=%+v", idToken, claims)
	}

	// Verify 不比较 nonce：nonce 不同的 token 也能通过，必须由调用方比较。
	other, err := verifier.Verify(ctx, p.idToken(t, p.key, clientID, "replayed", time.Now().Add(time.Hour)))
	if err != nil || other.Nonce == nonce {
		t.Fatalf("verify ignores nonce: nonce=%q err=%v", other.Nonce, err)
	}

	// 签名、受众和有效期由 Verify 检查。
	stranger, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"other audience": p.idToken(t, p.key, "another-app", nonce, time.Now().Add(time.Hour)),
		"expired":        p.idToken(t, p.key, clientID, nonce, time.Now().Add(-time.Minute)),
		"forged":         p.idToken(t, stranger, clientID, nonce, time.Now().Add(time.Hour)),
	} {
		if _, err := verifier.Verify(ctx, raw); err == nil {
			t.Errorf("%s: id_token accepted", name)
		}
	}
}
