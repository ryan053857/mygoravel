package sms_test

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/logging/logtest"
	"github.com/go-tenon/tenon/sms"
	"github.com/go-tenon/tenon/sms/smstest"
)

func code() sms.Message {
	return sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "123456"}}
}

func TestFailover(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		primary, backup := smstest.New(), smstest.New()
		rec := logtest.New()
		s := sms.Failover([]sms.Named{{Name: "aliyun", Sender: primary}, {Name: "tencent", Sender: backup}},
			sms.FailoverOptions{Cooldown: time.Minute, Logger: rec.Logger()})

		if err := s.Send(ctx, code()); err != nil || len(primary.Sent()) != 1 || len(backup.Sent()) != 0 {
			t.Fatalf("healthy: %v", err)
		}

		// 服务商的问题（余额不足等）：换用下一家。
		primary.FailWith(errors.New("aliyun sms: isv.AMOUNT_NOT_ENOUGH"))
		if err := s.Send(ctx, code()); err != nil || len(backup.Sent()) != 1 {
			t.Fatalf("failover: %v", err)
		}
		r := rec.Records()[0]
		if r.Level != slog.LevelWarn || r.Attr("sent_by") != "tencent" || r.Attr("component") != "sms" ||
			!strings.Contains(r.Attr("error.message").(string), "AMOUNT_NOT_ENOUGH") {
			t.Fatalf("record %+v", r)
		}

		// 冷却期内直接用备用的。
		primary.FailWith(nil)
		if err := s.Send(ctx, code()); err != nil || len(primary.Sent()) != 1 || len(backup.Sent()) != 2 {
			t.Fatalf("cooldown: %v", err)
		}
		// 冷却期过后重新先试主服务商，并记录恢复。
		time.Sleep(time.Minute)
		if err := s.Send(ctx, code()); err != nil || len(primary.Sent()) != 2 {
			t.Fatalf("recovered: %v", err)
		}
		if r := rec.Records()[1]; r.Message != "sms sender recovered" || r.Attr("sender") != "aliyun" {
			t.Fatalf("record %+v", r)
		}
	})
}

func TestFailoverDoesNotBypassProvider(t *testing.T) {
	ctx := context.Background()
	for name, err := range map[string]error{
		"rejected number": sms.ErrRejected.Wrap(errors.New("isv.MOBILE_NUMBER_ILLEGAL")),
		"number limited":  sms.ErrLimited.Wrap(errors.New("isv.BUSINESS_LIMIT_CONTROL")),
	} {
		primary, backup := smstest.New(), smstest.New()
		s := sms.Failover([]sms.Named{{Name: "aliyun", Sender: primary}, {Name: "tencent", Sender: backup}}, sms.FailoverOptions{})
		primary.FailWith(err)
		got := s.Send(ctx, code())
		if !errors.Is(got, err) || len(backup.Sent()) != 0 {
			t.Errorf("%s: %v, backup %d", name, got, len(backup.Sent()))
		}
		// 不让主服务商进入冷却：下一条短信仍然先试它。
		primary.FailWith(nil)
		if err := s.Send(ctx, code()); err != nil || len(primary.Sent()) != 1 {
			t.Errorf("%s: next send %v", name, err)
		}
	}

	primary, backup := smstest.New(), smstest.New()
	s := sms.Failover([]sms.Named{{Name: "aliyun", Sender: primary}, {Name: "tencent", Sender: backup}}, sms.FailoverOptions{})
	bad := code()
	bad.To = "13800138000"
	if err := s.Send(ctx, bad); !errors.Is(err, sms.ErrInvalid) {
		t.Errorf("invalid: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Send(canceled, code()); !errors.Is(err, context.Canceled) || len(backup.Sent()) != 0 {
		t.Errorf("canceled: %v", err)
	}
}

func TestFailoverAllFail(t *testing.T) {
	a, b := smstest.New(), smstest.New()
	a.FailWith(errors.New("connection refused"))
	b.FailWith(errors.New("InternalError.Timeout"))
	s := sms.Failover([]sms.Named{{Name: "aliyun", Sender: a}, {Name: "tencent", Sender: b}}, sms.FailoverOptions{Logger: slog.New(slog.DiscardHandler)})
	err := s.Send(context.Background(), code())
	if err == nil || !strings.Contains(err.Error(), "sms: aliyun: connection refused") || !strings.Contains(err.Error(), "sms: tencent: InternalError.Timeout") {
		t.Fatalf("%v", err)
	}
}

func TestFailoverPanics(t *testing.T) {
	ok := smstest.New()
	for name, senders := range map[string][]sms.Named{
		"empty":     nil,
		"no name":   {{Sender: ok}},
		"nil":       {{Name: "aliyun"}},
		"duplicate": {{Name: "aliyun", Sender: ok}, {Name: "aliyun", Sender: ok}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", name)
				}
			}()
			sms.Failover(senders, sms.FailoverOptions{})
		}()
	}
}
