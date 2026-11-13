// Package oauth 用第三方账号登录：OpenID Connect 提供方（Google、Apple、Microsoft、企业的身份系统等）用 Discover，
// 只支持 OAuth 2.0 的提供方（GitHub、微信等）用 New。结果是 login.External，再交给 login.Realm.External。
//
// 三种入口：
//
//   - 单页应用：API 用 Start 生成跳转地址和 Flow，单页应用保存 Flow；回调后把它和回调参数交给 API，Finish 换取 token 并读取账号。
//   - App 自己完成跳转：把授权码交给 API，Exchange 换取 token 并读取账号。
//   - App 原生登录（Google 登录、Sign in with Apple 等 SDK 直接返回 ID token）：IDToken 校验，只适用于 OpenID Connect。
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/login"
)

var (
	// ErrInvalid 表示这次第三方登录无效：state 不符、授权码无法换取 token、ID token 的签名、签发者、受众、有效期或 nonce 不对。
	ErrInvalid = errorx.Define(errorx.Unauthenticated, "oauth.invalid")
	// ErrDenied 表示用户在提供方的页面上取消了授权。
	ErrDenied = errorx.Define(errorx.InvalidArgument, "oauth.denied")
)

// Config 是在提供方登记的一个应用。
type Config struct {
	// Provider 是提供方的名称，作为 login.External.Provider，例如 google、apple、github。
	Provider string
	// Issuer 是 OpenID Connect 的签发者地址，例如 https://accounts.google.com、https://appleid.apple.com；
	// Discover 从它的发现文档中读取授权、token 端点和公钥地址。New 不使用它。
	Issuer string
	// ClientID、ClientSecret 是登记应用时得到的凭据。
	ClientID     string
	ClientSecret string
	// RedirectURL 是 Start、Finish 的回调地址，通常是单页应用中的回调页。
	RedirectURL string
	// ClientRedirectURLs 是 App 自己发起授权时登记的回调地址，Exchange 只接受这些地址。
	ClientRedirectURLs []string
	// Audiences 是 ID token 可以接受的受众（aud），默认只有 ClientID。
	// App 原生登录时，SDK 签发的 ID token 的受众是 App 自己的 client ID，例如 iOS、Android 各一个，加在这里。
	Audiences []string
	// Scopes 是申请的范围。Discover 默认 openid、email、profile；New 没有默认值，例如 GitHub 用 read:user、user:email。
	Scopes []string
	// NoPKCE 不发送 PKCE 参数，用于不支持 PKCE 的提供方，例如 Apple、微信。
	NoPKCE bool
	// FormPost 让提供方用表单 POST 提交回调（response_mode=form_post），例如 Apple 申请邮箱或姓名时要求这样做。
	FormPost bool
	// Client 发出全部请求：发现文档、校验 ID token 的公钥、换取 token，以及 Account 读取账号。
	// Issuer 来自租户后台填写的企业 SSO 等不可信来源时，必须使用 outbound 的客户端，拒绝访问内网地址；
	// 发现文档中的 token 端点同样不可信。为 nil 时使用 http.DefaultClient。
	Client *http.Client
}

// with 让 ctx 中的出站请求使用 Config.Client。
func (cfg Config) with(ctx context.Context) context.Context {
	if cfg.Client == nil {
		return ctx
	}
	return oidc.ClientContext(ctx, cfg.Client)
}

// Account 用换到的 token 读取第三方账号，用于只支持 OAuth 2.0 的提供方。client 会在请求中带上 token，
// 并使用 Config.Client 发出请求。返回值的 Provider 由 New 的 Config.Provider 填入；需要时把读到的原始资料放进 Claims。
type Account func(ctx context.Context, client *http.Client, tok *oauth2.Token) (login.External, error)

// Provider 是一个第三方登录提供方，可以并发使用。
type Provider struct {
	cfg      Config
	oauth2   oauth2.Config
	verifier *oidc.IDTokenVerifier // OpenID Connect
	account  Account               // 只支持 OAuth 2.0
}

// Discover 读取 cfg.Issuer 的发现文档，创建 OpenID Connect 提供方：账号来自 token 响应中的 ID token。
// 它会访问网络，应当在启动时调用一次，之后一直复用；校验 ID token 用的公钥按需下载并缓存。
func Discover(ctx context.Context, cfg Config) (*Provider, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	if cfg.Issuer == "" {
		return nil, errors.New("oauth: Config.Issuer is required")
	}
	// 发现文档之后的公钥下载使用这个 ctx，不能随请求取消。
	provider, err := oidc.NewProvider(cfg.with(context.WithoutCancel(ctx)), cfg.Issuer)
	if err != nil {
		return nil, errors.Wrapf(err, "oauth: discover %s", cfg.Issuer)
	}
	if cfg.Audiences == nil {
		cfg.Audiences = []string{cfg.ClientID}
	}
	if cfg.Scopes == nil {
		cfg.Scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	p := newProvider(cfg, provider.Endpoint())
	// 受众可以有多个，由 verify 比较。
	p.verifier = provider.Verifier(&oidc.Config{SkipClientIDCheck: true})
	return p, nil
}

// New 创建只支持 OAuth 2.0 的提供方，例如 GitHub（golang.org/x/oauth2/github 的 Endpoint）。
// 授权码流程与 Discover 相同，账号由 account 用换到的 token 读取。
func New(cfg Config, endpoint oauth2.Endpoint, account Account) (*Provider, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	if endpoint.AuthURL == "" || endpoint.TokenURL == "" || account == nil {
		return nil, errors.New("oauth: endpoint and account are required")
	}
	p := newProvider(cfg, endpoint)
	p.account = account
	return p, nil
}

func (cfg Config) check() error {
	if _, err := (login.External{Provider: cfg.Provider, Subject: "x"}).Identifier(); err != nil {
		return errors.Wrap(err, "oauth: Config.Provider")
	}
	if cfg.ClientID == "" {
		return errors.New("oauth: Config.ClientID is required")
	}
	return nil
}

func newProvider(cfg Config, endpoint oauth2.Endpoint) *Provider {
	return &Provider{cfg: cfg, oauth2: oauth2.Config{
		ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, Endpoint: endpoint,
		RedirectURL: cfg.RedirectURL, Scopes: cfg.Scopes,
	}}
}

// Name 返回 Config.Provider。
func (p *Provider) Name() string { return p.cfg.Provider }

// Flow 是一次授权码流程从跳转到回调需要保留的随机值，每个只能使用一次。
//
//   - State 在回调时比较，确认这次回调是本站发起的登录，防止攻击者把自己的授权码塞进用户的浏览器；
//   - Nonce 写进 ID token，确认它属于这一次登录（只用于 OpenID Connect）；
//   - Verifier 是 PKCE 的原值：跳转时只发送它的哈希，截获授权码的人无法换取 token。
type Flow struct {
	State    string
	Nonce    string
	Verifier string
}

// String 把 Flow 编码为一个字符串，交给单页应用保存；用 ParseFlow 还原。
func (f Flow) String() string { return f.State + "." + f.Nonce + "." + f.Verifier }

// ParseFlow 还原 Flow.String 的结果，格式不对时返回 ErrInvalid。
func ParseFlow(s string) (Flow, error) {
	parts := strings.Split(s, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" {
		return Flow{}, ErrInvalid.New()
	}
	return Flow{State: parts[0], Nonce: parts[1], Verifier: parts[2]}, nil
}

// Start 开始登录：返回跳转到提供方的地址，以及要保存到回调的 Flow。
func (p *Provider) Start() (string, Flow) {
	f := Flow{State: rand.Text(), Nonce: rand.Text()}
	var opts []oauth2.AuthCodeOption
	if p.verifier != nil {
		opts = append(opts, oidc.Nonce(f.Nonce))
	}
	if !p.cfg.NoPKCE {
		f.Verifier = oauth2.GenerateVerifier()
		opts = append(opts, oauth2.S256ChallengeOption(f.Verifier))
	}
	if p.cfg.FormPost {
		opts = append(opts, oauth2.SetAuthURLParam("response_mode", "form_post"))
	}
	return p.oauth2.AuthCodeURL(f.State, opts...), f
}

// Finish 处理回调：比较 state，用授权码和 PKCE verifier 换取 token，读取第三方账号。
// params 是回调的查询参数；FormPost 时是表单（r.PostForm）。用户取消授权时返回 ErrDenied。
func (p *Provider) Finish(ctx context.Context, f Flow, params url.Values) (login.External, error) {
	if f.State == "" || params.Get("state") != f.State {
		return login.External{}, ErrInvalid.New()
	}
	if params.Get("error") != "" {
		return login.External{}, ErrDenied.New()
	}
	return p.exchange(ctx, p.oauth2, params.Get("code"), f.Verifier, f.Nonce)
}

// Grant 是 App 自己完成授权后交给 API 的内容。
type Grant struct {
	Code string
	// Verifier 是客户端生成的 PKCE 原值。客户端没有 client secret，PKCE 是它唯一的保护，所以必填（NoPKCE 时除外）。
	Verifier string
	// RedirectURI 是客户端跳转时使用的回调地址，必须在 Config.ClientRedirectURLs 中。
	RedirectURI string
	// Nonce 是客户端放进授权请求的值，OpenID Connect 提供方必填。
	Nonce string
}

// Exchange 用客户端自己完成授权得到的授权码换取 token，读取第三方账号；client secret 只在服务器上使用。
// 回调地址不在 ClientRedirectURLs 中、缺少 verifier 或 nonce 时返回 ErrInvalid：
// 否则攻击者可以用发往其他地址的授权码换取 token。
func (p *Provider) Exchange(ctx context.Context, g Grant) (login.External, error) {
	if !slices.Contains(p.cfg.ClientRedirectURLs, g.RedirectURI) ||
		g.Verifier == "" && !p.cfg.NoPKCE || g.Nonce == "" && p.verifier != nil {
		return login.External{}, ErrInvalid.New()
	}
	conf := p.oauth2
	conf.RedirectURL = g.RedirectURI
	return p.exchange(ctx, conf, g.Code, g.Verifier, g.Nonce)
}

func (p *Provider) exchange(ctx context.Context, conf oauth2.Config, code, verifier, nonce string) (login.External, error) {
	if code == "" {
		return login.External{}, ErrInvalid.New()
	}
	ctx = p.cfg.with(ctx)
	var opts []oauth2.AuthCodeOption
	if !p.cfg.NoPKCE {
		opts = append(opts, oauth2.VerifierOption(verifier))
	}
	tok, err := conf.Exchange(ctx, code, opts...)
	if err != nil {
		return login.External{}, ErrInvalid.Wrap(err)
	}
	if p.account != nil {
		ext, err := p.account(ctx, conf.Client(ctx, tok), tok)
		if err != nil {
			return login.External{}, err
		}
		ext.Provider = p.cfg.Provider
		return ext, nil
	}
	raw, _ := tok.Extra("id_token").(string)
	return p.verify(ctx, raw, nonce)
}

// IDToken 校验 App 原生登录 SDK 返回的 ID token：签名、签发者、受众（Config.Audiences）、有效期，以及 nonce。
// 只适用于 Discover 创建的提供方。
//
// nonce 应当由服务器下发、App 放进登录请求，这里传入服务器保存的那个值，才能防止截获的 ID token 被重放；
// 有的 SDK 要求在请求中放 nonce 的 SHA-256，这时传入哈希值。nonce 为空时不比较。
func (p *Provider) IDToken(ctx context.Context, raw, nonce string) (login.External, error) {
	if p.verifier == nil {
		return login.External{}, errors.Newf("oauth: %s is not an OpenID Connect provider", p.cfg.Provider)
	}
	return p.verify(ctx, raw, nonce)
}

// verify 校验 ID token 并读取账号。go-oidc 的 Verify 不比较受众（有多个）和 nonce，在这里比较。
func (p *Provider) verify(ctx context.Context, raw, nonce string) (login.External, error) {
	if raw == "" {
		return login.External{}, ErrInvalid.New()
	}
	tok, err := p.verifier.Verify(ctx, raw)
	if err != nil {
		return login.External{}, ErrInvalid.Wrap(err)
	}
	if !slices.ContainsFunc(tok.Audience, func(aud string) bool { return slices.Contains(p.cfg.Audiences, aud) }) ||
		nonce != "" && tok.Nonce != nonce {
		return login.External{}, ErrInvalid.New()
	}
	var claims struct {
		Email         string   `json:"email"`
		EmailVerified flexBool `json:"email_verified"`
		Name          string   `json:"name"`
	}
	var all map[string]any
	if err := tok.Claims(&claims); err != nil {
		return login.External{}, ErrInvalid.Wrap(err)
	}
	if err := tok.Claims(&all); err != nil {
		return login.External{}, ErrInvalid.Wrap(err)
	}
	return login.External{
		Provider: p.cfg.Provider, Subject: tok.Subject,
		Email: claims.Email, EmailVerified: bool(claims.EmailVerified), Name: claims.Name, Claims: all,
	}, nil
}

// flexBool 接受 JSON 布尔值和字符串 "true"、"false"：Apple 的 email_verified 是字符串。
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	switch v := v.(type) {
	case bool:
		*b = flexBool(v)
	case string:
		*b = flexBool(v == "true")
	}
	return nil
}
