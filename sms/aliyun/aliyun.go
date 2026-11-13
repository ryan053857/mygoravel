// Package aliyun 是对接阿里云短信服务（Dysmsapi）的 sms.Sender，调用 SendSms 接口。
//
//	sender, err := aliyun.New(cfg.SMS.Aliyun, nil)
//	err = sender.Send(ctx, sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "123456"}})
//
// 签名和模板需要先在阿里云控制台申请并审核通过；Config.Templates 把应用的模板名对应到模板 Code，
// 模板中的变量名与 Message.Params 的键一致。RAM 用户需要 dysms:SendSms 权限。
package aliyun

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/internal/aliyunapi"
	"github.com/go-tenon/tenon/outbound"
	"github.com/go-tenon/tenon/sms"
)

// DefaultEndpoint 是 Config.Endpoint 为空时调用的接口域名。
const DefaultEndpoint = "dysmsapi.aliyuncs.com"

// Timeout 是 New 创建的默认客户端的超时。
const Timeout = 10 * time.Second

// version 是短信 OpenAPI 的版本。
const version = "2017-05-25"

// Config 是阿里云短信的配置段。
//
//	aliyun:
//	  access_key_id: ${ALIYUN_ACCESS_KEY_ID}
//	  access_key_secret: ${ALIYUN_ACCESS_KEY_SECRET}
//	  sign_name: 博客
//	  templates:
//	    login_code: SMS_465000001
type Config struct {
	AccessKeyID     string `yaml:"access_key_id"`
	AccessKeySecret string `yaml:"access_key_secret"`
	// Endpoint 是接口的域名，为空时是 DefaultEndpoint。
	Endpoint string `yaml:"endpoint"`
	// SignName 是审核通过的短信签名，必填。
	SignName string `yaml:"sign_name"`
	// Templates 把应用的模板名对应到审核通过的模板 Code，例如 login_code: SMS_465000001。
	Templates map[string]string `yaml:"templates"`
}

// Validate 检查必填项。
func (c Config) Validate() error {
	var errs []error
	if c.AccessKeyID == "" || c.AccessKeySecret == "" {
		errs = append(errs, errors.New("access_key_id and access_key_secret are required"))
	}
	if c.SignName == "" {
		errs = append(errs, errors.New("sign_name is required"))
	}
	if len(c.Templates) == 0 {
		errs = append(errs, errors.New("templates must have at least one template"))
	}
	if c.Endpoint != "" && strings.ContainsAny(c.Endpoint, "/:") {
		errs = append(errs, errors.Newf("endpoint %q must be a host name, such as %s", c.Endpoint, DefaultEndpoint))
	}
	return errors.Join(errs...)
}

// 服务商拒绝向这个号码发送的错误码：换服务商也发不到。
var rejected = map[string]bool{
	"isv.MOBILE_NUMBER_ILLEGAL":   true, // 号码格式错误
	"isv.BLACK_KEY_CONTROL_LIMIT": true, // 号码在黑名单中
	"isv.CUSTOMER_REFUSED":        true, // 用户退订
	"MOBILE_IN_BLACK":             true, // 运营商黑名单
	"MOBILE_NOT_ON_SERVICE":       true, // 停机、空号
	"MOBILE_ACCOUNT_ABNORMAL":     true, // 欠费、携号转网等
	"INVALID_NUMBER":              true, // 号码状态异常
	"USER_REJECT":                 true, // 用户退订或未开通
	"SP_NOT_BY_INTER_SMS":         true, // 用户未开通国际短信
}

// 服务商对这个号码的频率限制：换一家发会绕过它的防刷保护。
var limited = map[string]bool{
	"isv.BUSINESS_LIMIT_CONTROL": true, // 同一号码的发送频率超过限制
	"MOBILE_SEND_LIMIT":          true, // 同一号码的日、月发送量超过限制
}

// Sender 调用阿里云短信的 SendSms 接口，可以并发使用。
type Sender struct {
	cfg Config
	api *aliyunapi.Client
}

var _ sms.Sender = (*Sender)(nil)

// New 创建 Sender，不访问网络。client 为 nil 时使用 outbound.New 创建的客户端，超时为 Timeout；
// 测试时传入 outboundtest 的客户端。
func New(cfg Config, client *http.Client) (*Sender, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "aliyun sms")
	}
	host := cfg.Endpoint
	if host == "" {
		host = DefaultEndpoint
	}
	if client == nil {
		client = outbound.New(outbound.Options{Timeout: Timeout})
	}
	api := &aliyunapi.Client{Host: host, Version: version, AccessKeyID: cfg.AccessKeyID, AccessKeySecret: cfg.AccessKeySecret, HTTP: client}
	return &Sender{cfg: cfg, api: api}, nil
}

// Send 发送一条短信。号码被拒绝时返回 sms.ErrRejected，触发了号码的频率限制时返回 sms.ErrLimited；
// 余额不足、签名或模板未审核、系统错误等返回其他错误。模板名没有配置时返回错误，不调用接口。
func (s *Sender) Send(ctx context.Context, msg sms.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	code, ok := s.cfg.Templates[msg.Template]
	if !ok {
		return errors.Newf("aliyun sms: template %q is not configured", msg.Template)
	}
	params := url.Values{
		// 阿里云接受带 86 前缀的国内号码，国际号码写成国家码加号码，都不带 +。
		"PhoneNumbers": {strings.TrimPrefix(msg.To, "+")},
		"SignName":     {s.cfg.SignName},
		"TemplateCode": {code},
	}
	if len(msg.Params) > 0 {
		data, err := json.Marshal(msg.Params)
		if err != nil {
			return errors.Wrap(err, "aliyun sms: encode template params")
		}
		params.Set("TemplateParam", string(data))
	}
	resp, err := s.api.Call(ctx, "SendSms", params)
	if err != nil {
		return errors.Wrap(err, "aliyun sms")
	}
	if resp.Status == http.StatusOK && resp.Code == "OK" {
		return nil
	}
	err = errors.Wrap(resp.Error("SendSms"), "aliyun sms")
	switch {
	case rejected[resp.Code]:
		return sms.ErrRejected.Wrap(err)
	case limited[resp.Code]:
		return sms.ErrLimited.Wrap(err)
	}
	return err
}
