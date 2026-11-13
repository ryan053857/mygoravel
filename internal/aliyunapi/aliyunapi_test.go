package aliyunapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"

	"github.com/go-tenon/tenon/outbound/outboundtest"
)

func TestAuthorization(t *testing.T) {
	// 与官方 SDK（darabonba-openapi 的 GetAuthorization）对同一个请求计算的结果相同。
	headers := map[string]string{
		"host":                  "cloudpush.aliyuncs.com",
		"x-acs-action":          "PushV2",
		"x-acs-version":         "2016-08-01",
		"x-acs-date":            "2026-11-13T08:00:00Z",
		"x-acs-signature-nonce": "3c9a2f6e-0b1d-4e7a-9f1c-2d5e8a7b6c40",
		"x-acs-content-sha256":  "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"content-type":          "application/x-www-form-urlencoded",
		"user-agent":            "not signed",
	}
	got := authorization("POST", headers, headers["x-acs-content-sha256"], "testid", "testsecret")
	want := "ACS3-HMAC-SHA256 Credential=testid," +
		"SignedHeaders=content-type;host;x-acs-action;x-acs-content-sha256;x-acs-date;x-acs-signature-nonce;x-acs-version," +
		"Signature=cbcb6d3776b2f648b16a98c0137bbd9b37853244036d3550391a5ac0afb6f271"
	if got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestCall(t *testing.T) {
	fake := outboundtest.New(t)
	fake.On("POST https://dysmsapi.aliyuncs.com/").Reply(outboundtest.JSON(400, map[string]string{
		"Code": "isv.MOBILE_NUMBER_ILLEGAL", "Message": "非法手机号", "RequestId": "r1",
	}))
	c := &Client{Host: "dysmsapi.aliyuncs.com", Version: "2017-05-25", AccessKeyID: "testid", AccessKeySecret: "testsecret", HTTP: fake.Client()}
	resp, err := c.Call(context.Background(), "SendSms", url.Values{"PhoneNumbers": {"8613800138000"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 400 || resp.Code != "isv.MOBILE_NUMBER_ILLEGAL" || resp.RequestID != "r1" {
		t.Fatalf("response %+v", resp)
	}
	if msg := resp.Error("SendSms").Error(); !strings.Contains(msg, "HTTP 400 isv.MOBILE_NUMBER_ILLEGAL: 非法手机号 (request r1)") {
		t.Fatal(msg)
	}
	req := fake.Sent("POST https://dysmsapi.aliyuncs.com/")[0]
	sum := sha256.Sum256(req.Body)
	if req.Header.Get("x-acs-action") != "SendSms" || req.Header.Get("x-acs-version") != "2017-05-25" ||
		req.Header.Get("x-acs-content-sha256") != hex.EncodeToString(sum[:]) ||
		!strings.HasPrefix(req.Header.Get("Authorization"), "ACS3-HMAC-SHA256 Credential=testid,") {
		t.Fatalf("headers %v", req.Header)
	}
	if form, _ := req.Form(); form.Get("PhoneNumbers") != "8613800138000" {
		t.Fatalf("form %v", form)
	}
}
