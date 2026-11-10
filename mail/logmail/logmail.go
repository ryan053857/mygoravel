// Package logmail 提供把邮件写进日志而不真正发送的 mail.Mailer，用于本地开发。
package logmail

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ryan053857/mygoravel/mail"
)

// Mailer 以 Info 级别记录每封邮件的收件人、主题和正文。
type Mailer struct {
	logger *slog.Logger
}

var _ mail.Mailer = (*Mailer)(nil)

// New 创建写入 logger 的 Mailer；logger 为 nil 时使用 slog.Default()。
func New(logger *slog.Logger) *Mailer {
	if logger == nil {
		logger = slog.Default()
	}
	return &Mailer{logger: logger}
}

// Send 校验邮件并写一条日志。正文优先记录 Text，没有时记录 HTML。
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	if err := msg.Validate(); err != nil {
		return err
	}
	body := msg.Text
	if body == "" {
		body = msg.HTML
	}
	attrs := []slog.Attr{
		slog.String("to", join(msg.To)),
		slog.String("subject", msg.Subject),
		slog.String("body", body),
	}
	if !msg.From.IsZero() {
		attrs = append(attrs, slog.String("from", msg.From.String()))
	}
	if len(msg.Cc) > 0 {
		attrs = append(attrs, slog.String("cc", join(msg.Cc)))
	}
	if len(msg.Bcc) > 0 {
		attrs = append(attrs, slog.String("bcc", join(msg.Bcc)))
	}
	if len(msg.Attachments) > 0 {
		names := make([]string, len(msg.Attachments))
		for i, a := range msg.Attachments {
			names[i] = a.Filename
		}
		attrs = append(attrs, slog.Any("attachments", names))
	}
	m.logger.LogAttrs(ctx, slog.LevelInfo, "mail sent", attrs...)
	return nil
}

func join(list []mail.Address) string {
	parts := make([]string, len(list))
	for i, a := range list {
		parts[i] = a.String()
	}
	return strings.Join(parts, ", ")
}
