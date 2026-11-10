// Package resend 通过 Resend API 发送邮件，底层使用 github.com/resend/resend-go/v3。
package resend

import (
	"context"
	"strings"

	"github.com/cockroachdb/errors"
	resendsdk "github.com/resend/resend-go/v3"

	"github.com/ryan053857/mygoravel/mail"
)

// Config 是 Resend 发送配置。
type Config struct {
	From   mail.Address `yaml:"from"` // 默认发件人，域名需要在 Resend 中验证
	APIKey string       `yaml:"api_key"`
}

// Validate 检查配置完整。
func (c Config) Validate() error {
	var errs []error
	if err := c.From.Validate(); err != nil {
		errs = append(errs, errors.Wrap(err, "from"))
	}
	if strings.TrimSpace(c.APIKey) == "" {
		errs = append(errs, errors.New("api_key is required"))
	}
	return errors.Join(errs...)
}

// Mailer 通过 Resend 发送邮件，可并发使用。
type Mailer struct {
	client *resendsdk.Client
	from   mail.Address
}

var _ mail.Mailer = (*Mailer)(nil)

// New 用 cfg.APIKey 创建 Resend 客户端。
func New(cfg Config) (*Mailer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "resend")
	}
	return &Mailer{client: resendsdk.NewClient(cfg.APIKey), from: cfg.From}, nil
}

// NewWithClient 使用已创建的 SDK 客户端，例如通过 resendsdk.NewCustomClient 自定义 http.Client，
// 或在测试中修改 client.BaseURL。
func NewWithClient(client *resendsdk.Client, from mail.Address) (*Mailer, error) {
	if client == nil {
		return nil, errors.New("resend: client is required")
	}
	if err := from.Validate(); err != nil {
		return nil, errors.Wrap(err, "resend: from")
	}
	return &Mailer{client: client, from: from}, nil
}

// Client 返回底层 SDK 客户端，用于 Resend 特有的功能，如批量发送、定时发送。
func (m *Mailer) Client() *resendsdk.Client { return m.client }

type idempotencyKey struct{}

// WithIdempotencyKey 返回携带幂等键的 ctx。用同一个键重复发送时，Resend 只会发出一封。
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, idempotencyKey{}, key)
}

// Send 校验邮件并调用 Resend 的发送接口。
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	msg = msg.WithDefaultFrom(m.from)
	if err := msg.Validate(); err != nil {
		return err
	}
	req := &resendsdk.SendEmailRequest{
		From:    msg.From.String(),
		To:      addresses(msg.To),
		Cc:      addresses(msg.Cc),
		Bcc:     addresses(msg.Bcc),
		ReplyTo: msg.ReplyTo.String(),
		Subject: msg.Subject,
		Html:    msg.HTML,
		Text:    msg.Text,
		Headers: msg.Headers,
	}
	for _, a := range msg.Attachments {
		req.Attachments = append(req.Attachments, &resendsdk.Attachment{
			Filename: a.Filename, ContentType: a.ContentType, Content: a.Content,
		})
	}
	key, _ := ctx.Value(idempotencyKey{}).(string)
	if _, err := m.client.Emails.SendWithOptions(ctx, req, &resendsdk.SendEmailOptions{IdempotencyKey: key}); err != nil {
		return errors.Wrap(err, "resend: send")
	}
	return nil
}

func addresses(list []mail.Address) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, len(list))
	for i, a := range list {
		out[i] = a.String()
	}
	return out
}
