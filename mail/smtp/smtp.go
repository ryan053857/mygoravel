// Package smtp 通过 SMTP 服务器发送邮件，底层使用 github.com/wneessen/go-mail。
package smtp

import (
	"bytes"
	"context"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	gomail "github.com/wneessen/go-mail"

	"github.com/ryan053857/mygoravel/mail"
)

// TLS 取值。
const (
	TLSStartTLS = "starttls" // 连接后必须升级到 TLS，通常用于 587 端口
	TLSImplicit = "tls"      // 直接建立 TLS 连接，通常用于 465 端口
	TLSNone     = "none"     // 不加密，仅用于本地调试服务器（如 Mailpit）
)

// Config 是 SMTP 发送配置。Username 为空时不认证；认证需要 starttls 或 tls，
// go-mail 不会在未加密的连接上发送密码。
type Config struct {
	From     mail.Address  `yaml:"from"` // 默认发件人
	Host     string        `yaml:"host"`
	Port     int           `yaml:"port"`
	Username string        `yaml:"username"`
	Password string        `yaml:"password"`
	TLS      string        `yaml:"tls"`
	Timeout  time.Duration `yaml:"timeout"` // 连接与每次读写的超时
}

// SetDefaults 填充默认值：587 端口、starttls、10s 超时。
func (c *Config) SetDefaults() {
	*c = Config{Port: 587, TLS: TLSStartTLS, Timeout: 10 * time.Second}
}

// Validate 检查配置完整。
func (c Config) Validate() error {
	var errs []error
	if err := c.From.Validate(); err != nil {
		errs = append(errs, errors.Wrap(err, "from"))
	}
	if strings.TrimSpace(c.Host) == "" {
		errs = append(errs, errors.New("host is required"))
	}
	if c.Port < 1 || c.Port > 65535 {
		errs = append(errs, errors.New("port must be between 1 and 65535"))
	}
	if c.TLS != TLSStartTLS && c.TLS != TLSImplicit && c.TLS != TLSNone {
		errs = append(errs, errors.Newf("tls must be %s, %s or %s", TLSStartTLS, TLSImplicit, TLSNone))
	}
	if c.Timeout <= 0 {
		errs = append(errs, errors.New("timeout must be positive"))
	}
	return errors.Join(errs...)
}

// Mailer 每次 Send 建立一个新连接，可并发使用。
type Mailer struct {
	cfg Config
}

var _ mail.Mailer = (*Mailer)(nil)

// New 创建 SMTP 发送器，不会立即连接服务器。
func New(cfg Config) (*Mailer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "smtp")
	}
	return &Mailer{cfg: cfg}, nil
}

// Send 校验邮件并通过一次 SMTP 会话发出。
func (m *Mailer) Send(ctx context.Context, msg mail.Message) error {
	msg = msg.WithDefaultFrom(m.cfg.From)
	if err := msg.Validate(); err != nil {
		return err
	}
	gm, err := build(msg)
	if err != nil {
		return errors.Wrap(err, "smtp: build message")
	}
	client, err := gomail.NewClient(m.cfg.Host, m.options()...)
	if err != nil {
		return errors.Wrap(err, "smtp")
	}
	if err := client.DialAndSendWithContext(ctx, gm); err != nil {
		return errors.Wrap(err, "smtp: send")
	}
	return nil
}

func (m *Mailer) options() []gomail.Option {
	opts := []gomail.Option{gomail.WithPort(m.cfg.Port), gomail.WithTimeout(m.cfg.Timeout)}
	switch m.cfg.TLS {
	case TLSImplicit:
		opts = append(opts, gomail.WithSSL())
	case TLSNone:
		opts = append(opts, gomail.WithTLSPolicy(gomail.NoTLS))
	default:
		opts = append(opts, gomail.WithTLSPolicy(gomail.TLSMandatory))
	}
	if m.cfg.Username != "" {
		opts = append(opts,
			gomail.WithSMTPAuth(gomail.SMTPAuthAutoDiscover),
			gomail.WithUsername(m.cfg.Username),
			gomail.WithPassword(m.cfg.Password),
		)
	}
	return opts
}

func build(msg mail.Message) (*gomail.Msg, error) {
	gm := gomail.NewMsg(gomail.WithNoDefaultUserAgent())
	if err := gm.FromFormat(msg.From.Name, msg.From.Email); err != nil {
		return nil, err
	}
	for _, list := range []struct {
		addrs []mail.Address
		add   func(name, addr string) error
	}{{msg.To, gm.AddToFormat}, {msg.Cc, gm.AddCcFormat}, {msg.Bcc, gm.AddBccFormat}} {
		for _, a := range list.addrs {
			if err := list.add(a.Name, a.Email); err != nil {
				return nil, err
			}
		}
	}
	if !msg.ReplyTo.IsZero() {
		if err := gm.ReplyToFormat(msg.ReplyTo.Name, msg.ReplyTo.Email); err != nil {
			return nil, err
		}
	}
	gm.Subject(msg.Subject)
	gm.SetMessageID()
	gm.SetDate()
	switch {
	case msg.Text != "" && msg.HTML != "":
		gm.SetBodyString(gomail.TypeTextPlain, msg.Text)
		gm.AddAlternativeString(gomail.TypeTextHTML, msg.HTML)
	case msg.HTML != "":
		gm.SetBodyString(gomail.TypeTextHTML, msg.HTML)
	default:
		gm.SetBodyString(gomail.TypeTextPlain, msg.Text)
	}
	for _, a := range msg.Attachments {
		var opts []gomail.FileOption
		if a.ContentType != "" {
			opts = append(opts, gomail.WithFileContentType(gomail.ContentType(a.ContentType)))
		}
		if err := gm.AttachReader(a.Filename, bytes.NewReader(a.Content), opts...); err != nil {
			return nil, err
		}
	}
	for k, v := range msg.Headers {
		gm.SetGenHeader(gomail.Header(k), v)
	}
	return gm, nil
}
