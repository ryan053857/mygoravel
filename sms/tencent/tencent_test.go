package tencent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/outbound/outboundtest"
	"github.com/ryan053857/mygoravel/sms"
)

const api = "POST https://sms.tencentcloudapi.com/"

func config() Config {
	return Config{SecretID: "AKIDtestid", SecretKey: "testsecretkey", AppID: "1400000001", SignName: "博客",
		Templates: map[string]Template{"login_code": {ID: "123456", Params: []string{"code"}}}}
}

func code() sms.Message {
	return sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "654321"}}
}

func newSender(t *testing.T) (*Sender, *outboundtest.Transport) {
	t.Helper()
	fake := outboundtest.New(t)
	s, err := New(config(), fake.Client())
	if err != nil {
		t.Fatal(err)
	}
	return s, fake
}

func ok() outboundtest.Response {
	return outboundtest.JSON(200, map[string]any{"Response": map[string]any{
		"RequestId": "r1", "SendStatusSet": []map[string]any{{"Code": "Ok", "Message": "send success", "PhoneNumber": "+8613800138000"}},
	}})
}

func failed(code string) outboundtest.Response {
	return outboundtest.JSON(200, map[string]any{"Response": map[string]any{
		"RequestId": "r1", "SendStatusSet": []map[string]any{{"Code": code, "Message": "failed", "SerialNo": ""}},
	}})
}

func apiError(code string) outboundtest.Response {
	return outboundtest.JSON(200, map[string]any{"Response": map[string]any{
		"RequestId": "r1", "Error": map[string]string{"Code": code, "Message": "failed"},
	}})
}

func TestAuthorization(t *testing.T) {
	// 与腾讯云官方 Go SDK 对同一个请求计算的结果相同。
	body := `{"PhoneNumberSet":["+8613800138000"],"SmsSdkAppId":"1400000001","TemplateId":"123456","SignName":"博客","TemplateParamSet":["654321"]}`
	got := authorization("sms.tencentcloudapi.com", "application/json", []byte(body), time.Unix(1794514388, 0), "sms", "AKIDtestid", "testsecretkey")
	want := "TC3-HMAC-SHA256 Credential=AKIDtestid/2026-11-12/sms/tc3_request, SignedHeaders=content-type;host, " +
		"Signature=807a097ce746bc14f1f117cc7e17c7488637756a530cdaeb62066107583813d9"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSend(t *testing.T) {
	s, fake := newSender(t)
	fake.On(api).Reply(ok())
	if err := s.Send(context.Background(), code()); err != nil {
		t.Fatal(err)
	}
	req := fake.Sent(api)[0]
	var body request
	if err := json.Unmarshal(req.Body, &body); err != nil {
		t.Fatal(err)
	}
	if strings.Join(body.PhoneNumberSet, ",") != "+8613800138000" || body.SmsSdkAppID != "1400000001" || body.TemplateID != "123456" ||
		body.SignName != "博客" || strings.Join(body.TemplateParamSet, ",") != "654321" {
		t.Fatalf("body %s", req.Body)
	}
	h := req.Header
	if h.Get("X-TC-Action") != "SendSms" || h.Get("X-TC-Version") != "2021-01-11" || h.Get("X-TC-Region") != "ap-guangzhou" ||
		h.Get("X-TC-Timestamp") == "" || !strings.HasPrefix(h.Get("Authorization"), "TC3-HMAC-SHA256 Credential=AKIDtestid/") {
		t.Fatalf("headers %v", h)
	}
}

func TestErrors(t *testing.T) {
	s, fake := newSender(t)
	for name, tc := range map[string]struct {
		reply             outboundtest.Response
		rejected, limited bool
	}{
		"incorrect number": {reply: failed("InvalidParameterValue.IncorrectPhoneNumber"), rejected: true},
		"blacklist":        {reply: failed("FailedOperation.PhoneNumberInBlacklist"), rejected: true},
		"30s limit":        {reply: failed("LimitExceeded.PhoneNumberThirtySecondLimit"), limited: true},
		"daily limit":      {reply: failed("LimitExceeded.PhoneNumberDailyLimit"), limited: true},
		"no balance":       {reply: failed("FailedOperation.InsufficientBalanceInSmsPackage")},
		"template":         {reply: failed("FailedOperation.TemplateIncorrectOrUnapproved")},
		"api auth error":   {reply: apiError("AuthFailure.SecretIdNotFound")},
		"api number error": {reply: apiError("InvalidParameterValue.IncorrectPhoneNumber"), rejected: true},
		"internal":         {reply: apiError("InternalError.Timeout")},
		"no status":        {reply: outboundtest.JSON(200, map[string]any{"Response": map[string]any{"RequestId": "r1"}})},
		"gateway":          {reply: outboundtest.Text(502, "bad gateway")},
		"network":          {reply: outboundtest.Fail(errors.New("connection reset"))},
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
	missing := code()
	missing.Params = nil
	if err := s.Send(context.Background(), missing); !errors.Is(err, sms.ErrInvalid) {
		t.Fatalf("missing param: %v", err)
	}
	unknown := code()
	unknown.Template = "welcome"
	if err := s.Send(context.Background(), unknown); err == nil || errors.Is(err, sms.ErrInvalid) {
		t.Fatalf("unknown template: %v", err)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("%d requests", n)
	}
}

func TestValidate(t *testing.T) {
	for name, edit := range map[string]func(*Config){
		"no secret":      func(c *Config) { c.SecretKey = "" },
		"no app":         func(c *Config) { c.AppID = "" },
		"no templates":   func(c *Config) { c.Templates = nil },
		"no template id": func(c *Config) { c.Templates = map[string]Template{"login_code": {}} },
		"endpoint url":   func(c *Config) { c.Endpoint = "https://sms.tencentcloudapi.com" },
	} {
		cfg := config()
		edit(&cfg)
		if _, err := New(cfg, nil); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
