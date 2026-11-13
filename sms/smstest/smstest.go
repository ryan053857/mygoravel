// Package smstest 提供在内存中记录短信的 sms.Sender，用于测试。
package smstest

import (
	"context"
	"slices"
	"sync"

	"github.com/go-tenon/tenon/sms"
)

// Sender 记录每条通过 Validate 的短信，可并发使用。
type Sender struct {
	mu   sync.Mutex
	sent []sms.Message
	err  error
}

var _ sms.Sender = (*Sender)(nil)

// New 创建一个空的 Sender。
func New() *Sender { return &Sender{} }

// Send 校验并记录短信；设置了 FailWith 时返回该错误且不记录。
func (s *Sender) Send(ctx context.Context, msg sms.Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := msg.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.sent = append(s.sent, msg)
	return nil
}

// FailWith 让之后的 Send 都返回 err，模拟服务商故障或拒绝；传 nil 恢复正常。
func (s *Sender) FailWith(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.err = err
}

// Sent 返回已记录短信的副本，按发送顺序排列。
func (s *Sender) Sent() []sms.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.sent)
}

// Last 返回最后一条短信；还没有短信时返回零值和 false。
func (s *Sender) Last() (sms.Message, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.sent) == 0 {
		return sms.Message{}, false
	}
	return s.sent[len(s.sent)-1], true
}

// Reset 清空记录并取消 FailWith。
func (s *Sender) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent, s.err = nil, nil
}
