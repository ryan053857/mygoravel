// Package logintest 提供测试账号流程的工具：Outbox 记录 login.Options.Send 发出的验证码，
// TOTP 计算验证器上显示的数字。
package logintest

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-tenon/tenon/login"
)

// Outbox 记录发出的验证码，Send 可以直接作为 login.Options.Send。可并发使用。
type Outbox struct {
	mu   sync.Mutex
	sent []login.Code
	err  error
}

// NewOutbox 创建一个空的 Outbox。
func NewOutbox() *Outbox { return &Outbox{} }

// Send 记录验证码；设置了 FailWith 时返回该错误且不记录。
func (o *Outbox) Send(ctx context.Context, c login.Code) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return o.err
	}
	o.sent = append(o.sent, c)
	return nil
}

// FailWith 让之后的 Send 都返回 err，模拟邮件或短信发送失败；传 nil 恢复正常。
func (o *Outbox) FailWith(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
}

// Sent 返回已记录验证码的副本，按发送顺序排列。
func (o *Outbox) Sent() []login.Code {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.sent)
}

// Last 返回最后一个发给 to 的验证码，to 按 login.Identifier.Normalize 规范化后比较；没有时返回零值和 false。
func (o *Outbox) Last(to login.Identifier) (login.Code, bool) {
	if n, err := to.Normalize(); err == nil {
		to = n
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range slices.Backward(o.sent) {
		if c.To == to {
			return c, true
		}
	}
	return login.Code{}, false
}

// Reset 清空记录并取消 FailWith。
func (o *Outbox) Reset() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.sent, o.err = nil, nil
}

// TOTP 返回 base32 编码的密钥 secret（login.TOTPSetup.Secret）在当前时间的 6 位验证码，与验证器上显示的相同。
// secret 无效时 panic。
//
// 同一个 30 秒内的验证码只能使用一次：需要再输入一次时（例如启用后再登录），在 testing/synctest 中让时间前进 30 秒。
func TOTP(secret string) string {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		panic(fmt.Sprintf("logintest: invalid TOTP secret: %v", err))
	}
	m := hmac.New(sha1.New, key)
	_ = binary.Write(m, binary.BigEndian, time.Now().Unix()/30)
	sum := m.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	v := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", v%1_000_000)
}
