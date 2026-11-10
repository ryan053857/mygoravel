// Package mailtest 提供在内存中记录邮件的 mail.Mailer，用于测试。
package mailtest

import (
	"context"
	"slices"
	"sync"

	"github.com/ryan053857/mygoravel/mail"
)

// Mailer 记录每封通过 Validate 的邮件，可并发使用。
type Mailer struct {
	mu   sync.Mutex
	sent []mail.Message
	err  error
}

var _ mail.Mailer = (*Mailer)(nil)

// New 创建一个空的 Mailer。
func New() *Mailer { return &Mailer{} }

// Send 校验并记录邮件；设置了 FailWith 时返回该错误且不记录。
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := msg.Validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, msg)
	return nil
}

// FailWith 让之后的 Send 都返回 err；传 nil 恢复正常。
func (m *Mailer) FailWith(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.err = err
}

// Sent 返回已记录邮件的副本，按发送顺序排列。
func (m *Mailer) Sent() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.sent)
}

// Last 返回最后一封邮件；还没有邮件时返回零值和 false。
func (m *Mailer) Last() (mail.Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return mail.Message{}, false
	}
	return m.sent[len(m.sent)-1], true
}

// Reset 清空记录并取消 FailWith。
func (m *Mailer) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent, m.err = nil, nil
}
