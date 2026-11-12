// Package tencent 是对接腾讯云短信的 sms.Sender，调用 SendSms 接口（2021-01-11）。
//
//	sender, err := tencent.New(cfg.SMS.Tencent, nil)
//
// 签名和模板需要先在腾讯云控制台申请并审核通过。腾讯云的模板变量是按顺序的列表，
// Config.Templates 为每个模板写明变量的顺序，Message.Params 中的值按这个顺序填入。
package tencent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/outbound"
	"github.com/ryan053857/mygoravel/sms"
)

// DefaultEndpoint 是 Config.Endpoint 为空时调用的接口域名。
const DefaultEndpoint = "sms.tencentcloudapi.com"

// DefaultRegion 是 Config.Region 为空时的地域。
const DefaultRegion = "ap-guangzhou"

// Timeout 是 New 创建的默认客户端的超时。
const Timeout = 10 * time.Second

const (
	version     = "2021-01-11"
	service     = "sms"
	contentType = "application/json; charset=utf-8"
	maxResponse = 64 << 10
)

// Config 是腾讯云短信的配置段。
//
//	tencent:
//	  secret_id: ${TENCENT_SECRET_ID}
//	  secret_key: ${TENCENT_SECRET_KEY}
//	  app_id: "1400000001"
//	  sign_name: 博客
//	  templates:
//	    login_code: {id: "2000001", params: [code]}
type Config struct {
	SecretID  string `yaml:"secret_id"`
	SecretKey string `yaml:"secret_key"`
	// Region 是地域，为空时是 DefaultRegion。
	Region string `yaml:"region"`
	// Endpoint 是接口的域名，为空时是 DefaultEndpoint。
	Endpoint string `yaml:"endpoint"`
	// AppID 是短信应用的 SdkAppId，必填。
	AppID string `yaml:"app_id"`
	// SignName 是审核通过的签名内容（不是签名 ID）。国内短信必填，国际短信可以为空。
	SignName string `yaml:"sign_name"`
	// Templates 把应用的模板名对应到审核通过的模板，例如 login_code: {id: "2000001", params: [code]}。
	Templates map[string]Template `yaml:"templates"`
}

// Template 是一个腾讯云模板：模板 ID，以及模板变量依次取 Message.Params 中的哪个键。
type Template struct {
	ID     string   `yaml:"id"`
	Params []string `yaml:"params"`
}

// Validate 检查必填项。
func (c Config) Validate() error {
	var errs []error
	if c.SecretID == "" || c.SecretKey == "" {
		errs = append(errs, errors.New("secret_id and secret_key are required"))
	}
	if c.AppID == "" {
		errs = append(errs, errors.New("app_id is required"))
	}
	if len(c.Templates) == 0 {
		errs = append(errs, errors.New("templates must have at least one template"))
	}
	for name, t := range c.Templates {
		if t.ID == "" {
			errs = append(errs, errors.Newf("templates.%s.id is required", name))
		}
	}
	if c.Endpoint != "" && strings.ContainsAny(c.Endpoint, "/:") {
		errs = append(errs, errors.Newf("endpoint %q must be a host name, such as %s", c.Endpoint, DefaultEndpoint))
	}
	return errors.Join(errs...)
}

// 服务商拒绝向这个号码发送的错误码：换服务商也发不到。
var rejected = map[string]bool{
	"InvalidParameterValue.IncorrectPhoneNumber": true, // 号码格式错误
	"FailedOperation.PhoneNumberParseFail":       true, // 号码解析失败
	"FailedOperation.PhoneNumberInBlacklist":     true, // 号码在免打扰名单中
}

// 服务商对这个号码的频率限制：换一家发会绕过它的防刷保护。
var limited = map[string]bool{
	"LimitExceeded.PhoneNumberDailyLimit":            true,
	"LimitExceeded.PhoneNumberOneHourLimit":          true,
	"LimitExceeded.PhoneNumberThirtySecondLimit":     true,
	"LimitExceeded.PhoneNumberSameContentDailyLimit": true,
	"LimitExceeded.DeliveryFrequencyLimit":           true,
}

// Sender 调用腾讯云短信的 SendSms 接口，可以并发使用。
type Sender struct {
	cfg    Config
	host   string
	region string
	client *http.Client
}

var _ sms.Sender = (*Sender)(nil)

// New 创建 Sender，不访问网络。client 为 nil 时使用 outbound.New 创建的客户端，超时为 Timeout；
// 测试时传入 outboundtest 的客户端。
func New(cfg Config, client *http.Client) (*Sender, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "tencent sms")
	}
	s := &Sender{cfg: cfg, host: cfg.Endpoint, region: cfg.Region, client: client}
	if s.host == "" {
		s.host = DefaultEndpoint
	}
	if s.region == "" {
		s.region = DefaultRegion
	}
	if s.client == nil {
		s.client = outbound.New(outbound.Options{Timeout: Timeout})
	}
	return s, nil
}

type request struct {
	PhoneNumberSet   []string `json:"PhoneNumberSet"`
	SmsSdkAppID      string   `json:"SmsSdkAppId"`
	TemplateID       string   `json:"TemplateId"`
	SignName         string   `json:"SignName,omitempty"`
	TemplateParamSet []string `json:"TemplateParamSet,omitempty"`
}

type response struct {
	Response struct {
		RequestID     string `json:"RequestId"`
		Error         *status
		SendStatusSet []status
	}
}

type status struct {
	Code    string
	Message string
}

// Send 发送一条短信。号码被拒绝时返回 sms.ErrRejected，触发了号码的频率限制时返回 sms.ErrLimited；
// 余额不足、签名或模板未审核、系统错误等返回其他错误。模板名没有配置时返回错误；
// 短信缺少模板需要的变量时返回 sms.ErrInvalid，都不调用接口。
func (s *Sender) Send(ctx context.Context, msg sms.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	tpl, ok := s.cfg.Templates[msg.Template]
	if !ok {
		return errors.Newf("tencent sms: template %q is not configured", msg.Template)
	}
	req := request{PhoneNumberSet: []string{msg.To}, SmsSdkAppID: s.cfg.AppID, TemplateID: tpl.ID, SignName: s.cfg.SignName}
	for _, name := range tpl.Params {
		v, ok := msg.Params[name]
		if !ok {
			return errors.Wrapf(sms.ErrInvalid, "tencent sms: template %q needs parameter %q", msg.Template, name)
		}
		req.TemplateParamSet = append(req.TemplateParamSet, v)
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return errors.Wrap(err, "tencent sms: encode request")
	}
	resp, err := s.call(ctx, "SendSms", payload)
	if err != nil {
		return err
	}
	st := resp.Response.Error
	if st == nil {
		if len(resp.Response.SendStatusSet) == 0 {
			return errors.Newf("tencent sms: SendSms: no send status (request %s)", resp.Response.RequestID)
		}
		st = &resp.Response.SendStatusSet[0]
	}
	if st.Code == "Ok" {
		return nil
	}
	err = errors.Newf("tencent sms: SendSms: %s: %s (request %s)", st.Code, st.Message, resp.Response.RequestID)
	switch {
	case rejected[st.Code]:
		return sms.ErrRejected.Wrap(err)
	case limited[st.Code]:
		return sms.ErrLimited.Wrap(err)
	}
	return err
}

// call 以 TC3-HMAC-SHA256 签名调用接口 action，返回解析后的响应。
func (s *Sender) call(ctx context.Context, action string, payload []byte) (*response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+s.host+"/", bytes.NewReader(payload))
	if err != nil {
		return nil, errors.Wrap(err, "tencent sms: new request")
	}
	now := time.Now()
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Region", s.region)
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(now.Unix(), 10))
	req.Header.Set("Authorization", authorization(s.host, contentType, payload, now, service, s.cfg.SecretID, s.cfg.SecretKey))

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "tencent sms: %s", action)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return nil, errors.Wrapf(err, "tencent sms: %s: read response", action)
	}
	var r response
	if err := json.Unmarshal(data, &r); err != nil || resp.StatusCode != http.StatusOK {
		return nil, errors.Newf("tencent sms: %s: HTTP %d: %s", action, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return &r, nil
}
