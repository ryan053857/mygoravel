package oauthserver

import (
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

	"github.com/a-h/templ"
	"github.com/cockroachdb/errors"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

// authParams 是授权请求中原样带到同意页、再提交回来的参数。
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
	params    map[string]string
}

// redirectError 是可以跳回客户端的授权错误（RFC 6749 4.1.2.1）。
type redirectError struct {
	code, description string
}

func (e *redirectError) Error() string { return e.code + ": " + e.description }

// parse 校验授权请求。client_id 或 redirect_uri 不对时返回 ErrInvalidClient、ErrInvalidRedirect：
// 不能确认回调地址属于这个客户端，不能跳回去，呈现错误页；其他问题返回 *redirectError，带着 req 跳回客户端。
func (s *Server) parse(r *http.Request, values url.Values) (*authRequest, error) {
	params := map[string]string{}
	for _, name := range authParams {
		if v := values.Get(name); v != "" {
			params[name] = v
		}
	}
	cl, err := s.resolve(r.Context(), params["client_id"])
	if err != nil {
		s.logger.WarnContext(r.Context(), "oauth client rejected", "client_id", params["client_id"], "error", err)
		return nil, ErrInvalidClient.Wrap(err)
	}
	redirect := params["redirect_uri"]
	if redirect == "" || !cl.allows(redirect) {
		return nil, ErrInvalidRedirect.New()
	}
	req := &authRequest{client: cl, redirect: redirect, state: params["state"], challenge: params["code_challenge"], params: params}
	switch {
	case params["response_type"] != "code":
		return req, &redirectError{"unsupported_response_type", "response_type must be code"}
	case params["code_challenge_method"] != "S256" || !codeChallenge.MatchString(req.challenge):
		return req, &redirectError{"invalid_request", "PKCE with code_challenge_method S256 is required"}
	}
	if scope := strings.Fields(params["scope"]); len(scope) > 0 {
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
	req.resource = params["resource"]
	if req.resource == "" && len(s.opts.Resources) == 1 {
		req.resource = s.opts.Resources[0]
	}
	if !slices.Contains(s.opts.Resources, req.resource) {
		return req, &redirectError{"invalid_target", "unknown resource " + req.resource}
	}
	return req, nil
}

// authorize 呈现同意页。
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) error {
	noFrame(w)
	req, err := s.parse(r, r.URL.Query())
	if err != nil {
		return s.fail(w, r, req, err)
	}
	sub, _ := identity.FromContext(r.Context())
	props := ui.ConsentProps{
		Brand:        s.opts.Brand,
		ClientName:   req.client.name,
		RedirectHost: host(req.redirect),
		Account:      s.account(r, sub),
		Action:       AuthorizePath,
		Fields:       req.params,
	}
	for _, name := range req.scopes {
		props.Scopes = append(props.Scopes, ui.ConsentScope{Name: name, Description: s.scopes[name].Description})
	}
	render := s.opts.Consent
	if render == nil {
		render = func(_ *http.Request, p ui.ConsentProps) templ.Component { return ui.ConsentPage(p) }
	}
	return web.Render(w, r, http.StatusOK, render(r, props))
}

// decide 处理同意页的提交：重新校验授权请求，同意时签发授权码，带着它跳回客户端；拒绝时带着 access_denied 跳回。
func (s *Server) decide(w http.ResponseWriter, r *http.Request) error {
	noFrame(w)
	if err := r.ParseForm(); err != nil {
		return web.ErrBadForm.Wrap(err)
	}
	req, err := s.parse(r, r.PostForm)
	if err != nil {
		return s.fail(w, r, req, err)
	}
	if r.PostForm.Get("decision") != "allow" {
		return s.fail(w, r, req, &redirectError{"access_denied", "the user denied the request"})
	}
	sub, _ := identity.FromContext(r.Context())
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
	if _, err := s.db.NewInsert().Model(c).Exec(r.Context()); err != nil {
		return errors.Wrap(err, "oauthserver: save code")
	}
	s.redirect(w, r, req, url.Values{"code": {plain}})
	return nil
}

// fail 呈现授权错误：能跳回客户端的带着 error 跳回，其余交给页面的错误页。
func (s *Server) fail(w http.ResponseWriter, r *http.Request, req *authRequest, err error) error {
	var re *redirectError
	if req == nil || !errors.As(err, &re) {
		return err
	}
	s.redirect(w, r, req, url.Values{"error": {re.code}, "error_description": {re.description}})
	return nil
}

// redirect 跳回客户端的回调地址，附上 values、state 和 iss（RFC 9207）。
func (s *Server) redirect(w http.ResponseWriter, r *http.Request, req *authRequest, values url.Values) {
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
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (s *Server) account(r *http.Request, sub identity.Subject) string {
	if s.opts.Account != nil {
		return s.opts.Account(r.Context(), sub)
	}
	return sub.ID
}

// noFrame 禁止同意页被嵌入其他网站的框架，防止诱导点击。
// CSP 用 Add 追加为第二条策略：浏览器同时执行全部策略，web.Options.CSP 放开了 frame-ancestors 时同意页仍然不能被嵌入。
func noFrame(w http.ResponseWriter) {
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Add("Content-Security-Policy", "frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-store")
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
