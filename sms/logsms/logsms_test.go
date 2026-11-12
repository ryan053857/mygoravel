package logsms_test

import (
	"context"
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/logging/logtest"
	"github.com/ryan053857/mygoravel/sms"
	"github.com/ryan053857/mygoravel/sms/logsms"
)

func TestSend(t *testing.T) {
	rec := logtest.New()
	s := logsms.New(rec.Logger())
	msg := sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "123456"}}
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	r, ok := rec.Find("sms sent")
	if !ok || r.Attr("to") != "+8613800138000" || r.Attr("template") != "login_code" || r.Attr("params").(map[string]string)["code"] != "123456" {
		t.Fatalf("record %+v", r)
	}
	msg.To = "123"
	if err := s.Send(context.Background(), msg); !errors.Is(err, sms.ErrInvalid) {
		t.Fatalf("invalid: %v", err)
	}
}
