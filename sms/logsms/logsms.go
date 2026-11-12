// Package logsms 提供把短信写进日志而不真正发送的 sms.Sender，用于本地开发。
package logsms

import (
	"context"
	"log/slog"

	"github.com/ryan053857/mygoravel/sms"
)

// Sender 以 Info 级别记录每条短信的号码、模板和变量，开发时可以从日志里看到验证码。
type Sender struct {
	logger *slog.Logger
}

var _ sms.Sender = (*Sender)(nil)

// New 创建写入 logger 的 Sender；logger 为 nil 时使用 slog.Default()。
func New(logger *slog.Logger) *Sender {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sender{logger: logger}
}

// Send 校验短信并写一条 sms sent 日志。
func (s *Sender) Send(ctx context.Context, msg sms.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	s.logger.LogAttrs(ctx, slog.LevelInfo, "sms sent",
		slog.String("to", msg.To), slog.String("template", msg.Template), slog.Any("params", msg.Params))
	return nil
}
