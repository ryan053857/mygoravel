package aliyun_test

import (
	"context"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/outbound/outboundtest"
	"github.com/ryan053857/mygoravel/sms"
	"github.com/ryan053857/mygoravel/sms/aliyun"
)

const api = "POST https://dysmsapi.aliyuncs.com/"

func config() aliyun.Config {
	return aliyun.Config{AccessKeyID: "testid", AccessKeySecret: "testsecret", SignName: "博客",
		Templates: map[string]string{"login_code": "SMS_465000001"}}
}

func code() sms.Message {
	return sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "123456"}}
}

func newSender(t *testing.T) (*aliyun.Sender, *outboundtest.Transport) {
	t.Helper()
	fake := outboundtest.New(t)
	s, err := aliyun.New(config(), fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func TestSend(t *testing.T) {
	s, fake := newSender(t)
	fake.On(api).Reply(outboundtest.JSON(200, map[string]string{"Code": "OK", "Message": "OK", "BizId": "b1", "RequestId": "r1"}))
	if err := s.Send(context.Background(), code()); err != nil {
		t.Fatal(err)
	}
	req := fake.Sent(api)[0]
	form, err := req.Form()
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("PhoneNumbers") != "8613800138000" || form.Get("SignName") != "博客" ||
		form.Get("TemplateCode") != "SMS_465000001" || form.Get("TemplateParam") != `{"code":"123456"}` ||
		req.Header.Get("x-acs-action") != "SendSms" || req.Header.Get("x-acs-version") != "2017-05-25" {
		t.Fatalf("form %v headers %v", form, req.Header)
	}
}

func TestErrors(t *testing.T) {
	s, fake := newSender(t)
	for name, tc := range map[string]struct {
		reply    outboundtest.Response
		rejected bool
		limited  bool
	}{
		// 业务错误也是 HTTP 200，Code 不是 OK。
		"illegal number": {reply: outboundtest.JSON(200, map[string]string{"Code": "isv.MOBILE_NUMBER_ILLEGAL", "Message": "非法手机号"}), rejected: true},
		"blacklisted":    {reply: outboundtest.JSON(200, map[string]string{"Code": "isv.BLACK_KEY_CONTROL_LIMIT"}), rejected: true},
		"number limited": {reply: outboundtest.JSON(200, map[string]string{"Code": "isv.BUSINESS_LIMIT_CONTROL", "Message": "触发号码天级流控"}), limited: true},
		"no balance":     {reply: outboundtest.JSON(200, map[string]string{"Code": "isv.AMOUNT_NOT_ENOUGH"})},
		"bad template":   {reply: outboundtest.JSON(200, map[string]string{"Code": "isv.SMS_TEMPLATE_ILLEGAL"})},
		"system error":   {reply: outboundtest.JSON(200, map[string]string{"Code": "isp.SYSTEM_ERROR"})},
		"bad access key": {reply: outboundtest.JSON(404, map[string]string{"Code": "InvalidAccessKeyId.NotFound"})},
		"gateway":        {reply: outboundtest.Text(502, "bad gateway")},
		"network":        {reply: outboundtest.Fail(errors.New("connection reset"))},
	} {
		fake.On(api).Reply(tc.reply)
		err := s.Send(context.Background(), code())
		if err == nil || errors.Is(err, sms.ErrRejected) != tc.rejected || errors.Is(err, sms.ErrLimited) != tc.limited {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRequestNotSent(t *testing.T) {
	s, fake := newSender(t)
	unknown := code()
	unknown.Template = "welcome"
	if err := s.Send(context.Background(), unknown); err == nil || errors.Is(err, sms.ErrInvalid) {
		t.Fatalf("unknown template: %v", err)
	}
	bad := code()
	bad.To = "13800138000"
	if err := s.Send(context.Background(), bad); !errors.Is(err, sms.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestValidate(t *testing.T) {
	for name, edit := range map[string]func(*aliyun.Config){
		"no key":       func(c *aliyun.Config) { c.AccessKeySecret = "" },
		"no sign":      func(c *aliyun.Config) { c.SignName = "" },
		"no templates": func(c *aliyun.Config) { c.Templates = nil },
		"endpoint url": func(c *aliyun.Config) { c.Endpoint = "https://dysmsapi.aliyuncs.com" },
	} {
		cfg := config()
		edit(&cfg)
		if _, err := aliyun.New(cfg, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
