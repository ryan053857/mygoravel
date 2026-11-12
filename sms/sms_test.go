package sms_test

import (
	"testing"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/sms"
)

func TestValidate(t *testing.T) {
	ok := sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "123456"}}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, edit := range map[string]func(*sms.Message){
		"no plus":      func(m *sms.Message) { m.To = "13800138000" },
		"letters":      func(m *sms.Message) { m.To = "+86138001380ab" },
		"too short":    func(m *sms.Message) { m.To = "+861234" },
		"leading zero": func(m *sms.Message) { m.To = "+0861380013800" },
		"no template":  func(m *sms.Message) { m.Template = " " },
		"empty param":  func(m *sms.Message) { m.Params = map[string]string{"": "x"} },
	} {
		m := ok
		edit(&m)
		if err := m.Validate(); !errors.Is(err, sms.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestErrorKinds(t *testing.T) {
	// 被拒绝的号码是调用方的问题（422），频率限制是 429。
	if errorx.KindOf(sms.ErrRejected.New()) != errorx.InvalidArgument || errorx.KindOf(sms.ErrLimited.New()) != errorx.ResourceExhausted {
		t.Fatal("unexpected error kinds")
	}
}
