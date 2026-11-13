package oauthserver

import (
	"context"
	"net/http"
	"net/url"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx/rest"
	"github.com/ryan053857/mygoravel/identity"
)

// ConsentPath 是同意页两个接口的路径，相对于 Register 的 API。
const ConsentPath = "/oauth/consent"

var (
	// ErrInvalidRequest 表示授权请求的其他参数不合法，例如没有 PKCE、范围或资源未知。
	// 授权端点已经把这类请求带着错误跳回客户端，同意页的接口只会在 request 被改动过时遇到它。
	ErrInvalidRequest = errorx.Define(errorx.InvalidArgument, "oauth.invalid_request")
	// ErrRestrictedCredential 表示调用同意页接口的凭据不代表用户本人：有权限限制（个人访问令牌、OAuth 令牌），
	// 或者是代为登录（identity.Subject.Impersonator 不为零值）。只有用户本人的登录凭据才能授权其他应用。
	ErrRestrictedCredential = errorx.Define(errorx.PermissionDenied, "oauth.restricted_credential")
)

// Consent 是同意页要显示的授权请求。
type Consent struct {
	// ClientID 是申请授权的客户端，ClientName 是它的名称，例如 Claude。
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name"`
	// RedirectHost 是授权后跳转去的主机，例如 claude.ai。
	RedirectHost string `json:"redirect_host"`
	// Resource 是令牌可以访问的资源，例如 https://blog.example.com/mcp。
	Resource string `json:"resource"`
	// Scopes 是申请的范围及说明。
	Scopes []Scope `json:"scopes"`
}

// Decision 是用户在同意页上的选择。
type Decision struct {
	// Request 是同意页地址中的 request 参数，原样交回。
	Request string `json:"request" minLength:"1"`
	// Approve 为 true 表示同意，false 表示拒绝。
	Approve bool `json:"approve"`
}

// Redirect 是用户做出选择后浏览器要跳转去的地址：同意时带着授权码，拒绝时带着 access_denied，都回到客户端。
type Redirect struct {
	URL string `json:"url"`
}

type consentInput struct {
	Request string `query:"request" required:"true" minLength:"1" doc:"同意页地址中的 request 参数"`
}

type consentOutput struct{ Body Consent }

type decisionInput struct{ Body Decision }

type redirectOutput struct{ Body Redirect }

// Register 在 api 上注册同意页使用的两个接口，auth 要求登录，通常是 rest.RequireBearer(api, tokens.Validate)：
//
//	GET  /oauth/consent?request=…  读取授权请求，显示在同意页上（oauth-consent-get）
//	POST /oauth/consent            同意或拒绝，返回浏览器要跳转去的地址（oauth-consent-decide）
//
// 能授权的就是 auth 接受的账号体系。只接受用户本人的登录凭据：主体的 Scopes 不为 nil（个人访问令牌、OAuth 令牌），
// 或者是代为登录时，返回 ErrRestrictedCredential。客服代为登录排查问题时，不能替用户把其他应用连上账号。
func (s *Server) Register(api *rest.API, auth func(*huma.Operation)) {
	get := huma.Operation{
		OperationID: "oauth-consent-get", Method: http.MethodGet, Path: ConsentPath,
		Summary: "读取授权请求", Tags: []string{"oauth"},
	}
	auth(&get)
	rest.Register(api, get, s.consent)

	decide := huma.Operation{
		OperationID: "oauth-consent-decide", Method: http.MethodPost, Path: ConsentPath,
		Summary: "同意或拒绝授权", Tags: []string{"oauth"},
	}
	auth(&decide)
	rest.Register(api, decide, s.decide)
}

func (s *Server) consent(ctx context.Context, in *consentInput) (*consentOutput, error) {
	req, err := s.request(ctx, in.Request)
	if err != nil {
		return nil, err
	}
	c := Consent{ClientID: req.client.id, ClientName: req.client.name, RedirectHost: host(req.redirect), Resource: req.resource}
	for _, name := range req.scopes {
		c.Scopes = append(c.Scopes, s.scopes[name])
	}
	return &consentOutput{Body: c}, nil
}

// decide 重新校验授权请求，同意时签发授权码，拒绝时带着 access_denied，返回跳回客户端的地址。
func (s *Server) decide(ctx context.Context, in *decisionInput) (*redirectOutput, error) {
	req, err := s.request(ctx, in.Body.Request)
	if err != nil {
		return nil, err
	}
	if !in.Body.Approve {
		re := &redirectError{"access_denied", "the user denied the request"}
		return &redirectOutput{Body: Redirect{URL: s.back(req, re.values())}}, nil
	}
	sub, _ := identity.FromContext(ctx)
	u, err := s.issue(ctx, req, sub)
	if err != nil {
		return nil, err
	}
	return &redirectOutput{Body: Redirect{URL: u}}, nil
}

// request 检查调用者的凭据，解析并校验同意页交回的授权请求。
func (s *Server) request(ctx context.Context, raw string) (*authRequest, error) {
	sub, ok := identity.FromContext(ctx)
	if !ok || sub.Scopes != nil || !sub.Impersonator.IsZero() {
		return nil, ErrRestrictedCredential.New()
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, ErrInvalidRequest.Wrap(err)
	}
	req, err := s.parse(ctx, values)
	if re := (*redirectError)(nil); errors.As(err, &re) {
		return nil, ErrInvalidRequest.Wrap(err)
	}
	return req, err
}
