package oauthserver

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/identity"
)

// authParams 是授权请求中带到同意页（request 参数）、再随同意页的接口交回的参数。
var authParams = []string{"response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "scope", "resource"}

// codeChallenge 是 S256 的 code_challenge：SHA-256 的 base64url（无填充），43 个字符。
var codeChallenge = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

// code 是 oauth_codes 表中的一个授权码。
type code struct {
	bun.BaseModel `bun:"table:oauth_codes,alias:oc"`

	CodeHash         string    `bun:"code_hash,pk"`
	ClientID         string    `bun:"client_id,notnull"`
	ClientName       string    `bun:"client_name,notnull"`
	RedirectURI      string    `bun:"redirect_uri,notnull"`
	CodeChallenge    string    `bun:"code_challenge,notnull"`
	Scopes           string    `bun:"scopes,notnull"`
	Resource         string    `bun:"resource,notnull"`
	SubjectType      string    `bun:"subject_type,notnull"`
	SubjectID        string    `bun:"subject_id,notnull"`
	Tenant           string    `bun:"tenant,notnull"`
	ImpersonatorType string    `bun:"impersonator_type,notnull"`
	ImpersonatorID   string    `bun:"impersonator_id,notnull"`
	CreatedAt        time.Time `bun:"created_at,notnull"`
	ExpiresAt        time.Time `bun:"expires_at,notnull"`
}

// authRequest 是校验通过的授权请求。
type authRequest struct {
	client    *client
	redirect  string
	state     string
	challenge string
	scopes    []string
	resource  string
	params    url.Values
}

// redirectError 是可以跳回客户端的授权错误（RFC 6749 4.1.2.1）。
type redirectError struct {
	code, description string
}

func (e *redirectError) Error() string { return e.code + ": " + e.description }

// parse 校验授权请求。client_id 或 redirect_uri 不对时返回 ErrInvalidClient、ErrInvalidRedirect：
// 不能确认回调地址属于这个客户端，不能跳回去；其他问题返回 *redirectError，可以带着 req 跳回客户端。
func (s *Server) parse(ctx context.Context, values url.Values) (*authRequest, error) {
	params := url.Values{}
	for _, name := range authParams {
		if v := values.Get(name); v != "" {
			params.Set(name, v)
		}
	}
	cl, err := s.resolve(ctx, params.Get("client_id"))
	if err != nil {
		s.logger.WarnContext(ctx, "oauth client rejected", "client_id", params.Get("client_id"), "error", err)
		return nil, ErrInvalidClient.Wrap(err)
	}
	redirect := params.Get("redirect_uri")
	if redirect == "" || !cl.allows(redirect) {
		return nil, ErrInvalidRedirect.New()
	}
	req := &authRequest{client: cl, redirect: redirect, state: params.Get("state"), challenge: params.Get("code_challenge"), params: params}
	switch {
	case params.Get("response_type") != "code":
		return req, &redirectError{"unsupported_response_type", "response_type must be code"}
	case params.Get("code_challenge_method") != "S256" || !codeChallenge.MatchString(req.challenge):
		return req, &redirectError{"invalid_request", "PKCE with code_challenge_method S256 is required"}
	}
	if scope := strings.Fields(params.Get("scope")); len(scope) > 0 {
		for _, sc := range scope {
			if _, ok := s.scopes[sc]; !ok {
				return req, &redirectError{"invalid_scope", "unknown scope " + sc}
			}
		}
		slices.Sort(scope)
		req.scopes = slices.Compact(scope)
	} else {
		req.scopes = s.scopeNames()
	}
	req.resource = params.Get("resource")
	if req.resource == "" && len(s.opts.Resources) == 1 {
		req.resource = s.opts.Resources[0]
	}
	if !slices.Contains(s.opts.Resources, req.resource) {
		return req, &redirectError{"invalid_target", "unknown resource " + req.resource}
	}
	return req, nil
}

// authorize 校验授权请求，把它交给单页应用的同意页：合法的请求编码为 request 参数；
// 能跳回客户端的错误直接带着 error 跳回；client_id 或 redirect_uri 不对时不能跳回，把错误码交给同意页显示。
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	req, err := s.parse(r.Context(), r.URL.Query())
	var re *redirectError
	switch {
	case err == nil:
		http.Redirect(w, r, s.consentURL(url.Values{"request": {req.params.Encode()}}), http.StatusFound)
	case req != nil && errors.As(err, &re):
		http.Redirect(w, r, s.back(req, re.values()), http.StatusFound)
	default:
		http.Redirect(w, r, s.consentURL(url.Values{"error": {errorx.CodeOf(err)}}), http.StatusFound)
	}
}

// issue 为 sub 签发授权码，返回带着它跳回客户端的地址。
func (s *Server) issue(ctx context.Context, req *authRequest, sub identity.Subject) (string, error) {
	plain := randomCode()
	now := time.Now().UTC()
	c := &code{
		CodeHash: hashCode(plain), ClientID: req.client.id, ClientName: req.client.name,
		RedirectURI: req.redirect, CodeChallenge: req.challenge,
		Scopes: strings.Join(req.scopes, " "), Resource: req.resource,
		SubjectType: string(sub.Type), SubjectID: sub.ID, Tenant: sub.Tenant,
		ImpersonatorType: string(sub.Impersonator.Type), ImpersonatorID: sub.Impersonator.ID,
		CreatedAt: now, ExpiresAt: now.Add(DefaultCodeTTL),
	}
	if _, err := s.db.NewInsert().Model(c).Exec(ctx); err != nil {
		return "", errors.Wrap(err, "oauthserver: save code")
	}
	return s.back(req, url.Values{"code": {plain}}), nil
}

func (e *redirectError) values() url.Values {
	return url.Values{"error": {e.code}, "error_description": {e.description}}
}

// back 返回跳回客户端回调地址的 URL，附上 values、state 和 iss（RFC 9207）。
func (s *Server) back(req *authRequest, values url.Values) string {
	u, _ := url.Parse(req.redirect)
	q := u.Query()
	for k, v := range values {
		q[k] = v
	}
	if req.state != "" {
		q.Set("state", req.state)
	}
	q.Set("iss", s.issuer)
	u.RawQuery = q.Encode()
	return u.String()
}

// consentURL 返回 Options.ConsentURL 附上 values 的地址。
func (s *Server) consentURL(values url.Values) string {
	u, _ := url.Parse(s.opts.ConsentURL)
	q := u.Query()
	for k, v := range values {
		q[k] = v
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func host(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Host
}

// randomCode 生成授权码：32 字节随机数的 base64url。
func randomCode() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashCode(c string) string {
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}
