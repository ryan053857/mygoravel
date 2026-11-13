// Package sms 发送短信。国内短信只能用审核过的签名和模板发送，所以一条短信是收件号码、模板名和模板变量，
// 签名、模板 ID 由各个驱动按配置填写：sms/aliyun 对接阿里云短信，sms/tencent 对接腾讯云短信，
// sms/logsms 只记录日志，用于开发；sms/smstest 记录短信，用于测试。
//
//	err := sender.Send(ctx, sms.Message{To: "+8613800138000", Template: "login_code", Params: map[string]string{"code": "123456"}})
//
// Failover 组合多个服务商，前一个出问题时换下一个，用于验证码这类同步发送、用户在等的短信。
package sms

import (
	"context"
	"regexp"
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/go-tenon/tenon/errorx"
)

var (
	// ErrInvalid 表示短信本身不合法：号码不是 E.164 格式、模板名为空、变量名为空。换哪个服务商都一样。
	ErrInvalid = errors.New("sms: invalid message")
	// ErrRejected 表示服务商拒绝向这个号码发送：号码不存在或格式不被接受、停机、在黑名单中、用户退订。
	// 换服务商也发不到，Failover 不会换用下一个。
	ErrRejected = errorx.Define(errorx.InvalidArgument, "sms.rejected")
	// ErrLimited 表示触发了服务商对这个号码的频率限制，例如同一号码 30 秒内只能收一条。
	// 这是服务商的防刷保护，Failover 不会换用下一个服务商绕过它。
	ErrLimited = errorx.Define(errorx.ResourceExhausted, "sms.limited")
)

// e164 是 E.164 格式的号码：+ 加国家码和号码，共 8 到 15 位数字。
var e164 = regexp.MustCompile(`^\+[1-9][0-9]{7,14}$`)

// Sender 发送短信。实现必须遵守 ctx 的取消，并在发送前调用 Message.Validate。
type Sender interface {
	Send(ctx context.Context, msg Message) error
}

// Message 是一条短信。它可以完整地序列化为 JSON，便于交给队列发送。
type Message struct {
	// To 是 E.164 格式的号码，例如 +8613800138000。login 的手机号已经是这个格式。
	To string `json:"to"`
	// Template 是应用给模板起的名字，例如 login_code。驱动按配置换成服务商的模板 ID。
	Template string `json:"template"`
	// Params 是模板变量，例如 {"code": "123456"}。
	Params map[string]string `json:"params,omitempty"`
}

// Validate 检查号码是 E.164 格式、模板名不为空、变量名不为空。
func (m Message) Validate() error {
	var errs []error
	if !e164.MatchString(m.To) {
		errs = append(errs, errors.Wrapf(ErrInvalid, "phone number %q must be E.164, e.g. +8613800138000", m.To))
	}
	if strings.TrimSpace(m.Template) == "" {
		errs = append(errs, errors.Wrap(ErrInvalid, "template is required"))
	}
	for k := range m.Params {
		if strings.TrimSpace(k) == "" {
			errs = append(errs, errors.Wrap(ErrInvalid, "template parameter name is empty"))
		}
	}
	return errors.Join(errs...)
}
