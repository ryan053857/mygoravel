// Package captcha 在服务端校验人机验证的结果，用于自助注册、发送验证码和公开表单，挡住批量提交的机器人。
// 默认对接 Cloudflare Turnstile；hCaptcha 的校验接口与它相同，把 Config.Endpoint 设为 HCaptcha 即可。
//
//	bots, err := captcha.New(cfg.Captcha, nil)
//	// 处理器中，token 是前端组件交给表单的值
//	if err := bots.Verify(ctx, in.Body.Captcha, "signup", in.ip); err != nil {
//	    return nil, err // captcha.ErrFailed → 422；captcha.ErrUnavailable → 503
//	}
//
// 校验服务连不上时返回 ErrUnavailable，请求不会被放行。
package captcha

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/errorx"
	"github.com/go-tenon/tenon/outbound"
)

var (
	// ErrFailed 表示没有通过验证：没有提交 token，token 无效、过期或已经用过，或者站点、表单与预期不符。
	// 客户端应该重置验证组件，让用户重新验证。
	ErrFailed = errorx.Define(errorx.InvalidArgument, "captcha.failed")
	// ErrUnavailable 表示校验服务暂时不可用：连不上、超时或返回了异常的响应。
	ErrUnavailable = errorx.Define(errorx.Unavailable, "captcha.unavailable")
)

// 校验接口的地址。
const (
	Turnstile = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	HCaptcha  = "https://api.hcaptcha.com/siteverify"
)

// Timeout 是 New 创建的默认客户端的超时。
const Timeout = 10 * time.Second

// maxToken 是 token 的最大长度，Turnstile 的 token 不超过 2048 个字符。
const maxToken = 2048

// Config 是人机验证配置段。
//
//	captcha:
//	  secret: ${TURNSTILE_SECRET}
//	  hostnames: [blog.example.com]
type Config struct {
	// Secret 是服务端密钥（secret key），不是前端组件使用的 site key。
	Secret string `yaml:"secret"`
	// Hostnames 是允许的站点域名，验证结果中的 hostname 必须是其中之一；为空时不检查。
	// App 中的 Turnstile 返回的 hostname 不是网站域名，同时用于 App 时需要把它也列进来。
	Hostnames []string `yaml:"hostnames"`
	// Endpoint 是校验接口的地址，为空时是 Turnstile。
	Endpoint string `yaml:"endpoint"`
}

// Validate 检查 secret 已填写，endpoint 是 https 地址。
func (c Config) Validate() error {
	if c.Secret == "" {
		return errors.New("secret is required")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return errors.Newf("endpoint %q must be an https address", c.Endpoint)
		}
	}
	return nil
}

// Verifier 调用校验接口检查 token，可以并发使用。
type Verifier struct {
	cfg      Config
	endpoint string
	client   *http.Client
}

// New 创建 Verifier，不访问网络。client 为 nil 时使用 outbound.New 创建的客户端，超时为 Timeout；
// 测试时传入 outboundtest 的客户端。
func New(cfg Config, client *http.Client) (*Verifier, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "captcha")
	}
	endpoint := cfg.Endpoint
	if endpoint == "" {
		endpoint = Turnstile
	}
	if client == nil {
		client = outbound.New(outbound.Options{Timeout: Timeout})
	}
	return &Verifier{cfg: cfg, endpoint: endpoint, client: client}, nil
}

// result 是校验接口的响应，Turnstile 和 hCaptcha 共用这些字段。
type result struct {
	Success    bool     `json:"success"`
	ErrorCodes []string `json:"error-codes"`
	Hostname   string   `json:"hostname"`
	Action     string   `json:"action"`
}

// Verify 校验前端组件交给表单的 token。
//
// action 是前端组件设置的 action（Turnstile 的 data-action），用来区分表单：注册页的 token 不能拿来提交联系表单。
// 为空时不检查；hCaptcha 不返回 action，需要传空字符串。remoteIP 是客户端地址，可以为空。
//
// token 只能校验一次，第二次校验返回 ErrFailed，所以同一个请求中只调用一次。
// 没有通过时返回 ErrFailed；校验服务不可用时返回 ErrUnavailable；密钥错误返回普通错误，由日志报告配置问题。
func (v *Verifier) Verify(ctx context.Context, token, action, remoteIP string) error {
	if token == "" || len(token) > maxToken {
		return ErrFailed.New()
	}
	form := url.Values{"secret": {v.cfg.Secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return errors.Wrap(err, "captcha: new request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := v.client.Do(req)
	if err != nil {
		return ErrUnavailable.Wrap(errors.Wrap(err, "captcha: siteverify"))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ErrUnavailable.Wrap(errors.Newf("captcha: siteverify returned %s", resp.Status))
	}
	var r result
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&r); err != nil {
		return ErrUnavailable.Wrap(errors.Wrap(err, "captcha: decode siteverify response"))
	}
	if !r.Success {
		return failure(r.ErrorCodes)
	}
	if len(v.cfg.Hostnames) > 0 && !slices.Contains(v.cfg.Hostnames, r.Hostname) {
		return ErrFailed.Wrap(errors.Newf("captcha: hostname %q is not allowed", r.Hostname))
	}
	if action != "" && r.Action != action {
		return ErrFailed.Wrap(errors.Newf("captcha: action %q, want %q", r.Action, action))
	}
	return nil
}

// failure 把校验接口的错误码转换为错误：密钥问题是配置错误，服务内部错误是暂时不可用，其余都是没有通过。
func failure(codes []string) error {
	cause := errors.Newf("captcha: siteverify error codes %v", codes)
	for _, c := range codes {
		switch c {
		case "missing-input-secret", "invalid-input-secret", "sitekey-secret-mismatch":
			return errors.Wrap(cause, "captcha: check the secret")
		case "internal-error":
			return ErrUnavailable.Wrap(cause)
		}
	}
	return ErrFailed.Wrap(cause)
}
