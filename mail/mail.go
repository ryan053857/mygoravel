package mail

import (
	"context"
	netmail "net/mail"
	"strings"

	"github.com/cockroachdb/errors"
)

// ErrInvalid 表示邮件内容不完整或包含非法地址、头部。
var ErrInvalid = errors.New("mail: invalid message")

// Mailer 把邮件发出去。实现必须遵守 ctx 的取消，并在发送前调用 Message.Validate。
type Mailer interface {
	Send(ctx context.Context, msg Message) error
}

// Address 是一个邮件地址。在 YAML 和 JSON 中写成字符串："Blog <no-reply@example.com>" 或 "no-reply@example.com"。
type Address struct {
	Name  string
	Email string
}

// ParseAddress 解析 "Name <email>" 或 "email" 形式的地址。
func ParseAddress(s string) (Address, error) {
	a, err := netmail.ParseAddress(s)
	if err != nil {
		return Address{}, errors.Wrapf(ErrInvalid, "address %q", s)
	}
	return Address{Name: a.Name, Email: a.Address}, nil
}

// String 返回 "Name <email>"；没有名字时只返回 email，零值返回空字符串。
// 名字保留原始 UTF-8，含特殊字符时加引号。
func (a Address) String() string {
	if a.Name == "" {
		return a.Email
	}
	name := a.Name
	if strings.ContainsAny(name, "()<>[]:;@\\,.\"") {
		name = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(name) + `"`
	}
	return name + " <" + a.Email + ">"
}

// MarshalText 与 String 相同。
func (a Address) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText 用 ParseAddress 解析；空字符串得到零值。
func (a *Address) UnmarshalText(text []byte) error {
	if strings.TrimSpace(string(text)) == "" {
		*a = Address{}
		return nil
	}
	parsed, err := ParseAddress(string(text))
	if err != nil {
		return err
	}
	*a = parsed
	return nil
}

// IsZero 报告地址是否未设置。
func (a Address) IsZero() bool { return a == Address{} }

// Validate 检查 Email 是一个合法的裸地址，Name 不含换行。
func (a Address) Validate() error {
	parsed, err := netmail.ParseAddress(a.Email)
	if err != nil || parsed.Address != a.Email || parsed.Name != "" || strings.ContainsAny(a.Name, "\r\n") {
		return errors.Wrapf(ErrInvalid, "address %q", a.String())
	}
	return nil
}

// Attachment 是一个附件。Content 在 JSON 中为 base64；ContentType 为空时由驱动按文件名推断。
type Attachment struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
	Content     []byte `json:"content"`
}

// Message 是一封邮件。它可以完整地序列化为 JSON 再还原，便于交给队列发送。
type Message struct {
	From        Address           `json:"from,omitzero"` // 为空时使用驱动配置的默认发件人
	To          []Address         `json:"to"`
	Cc          []Address         `json:"cc,omitempty"`
	Bcc         []Address         `json:"bcc,omitempty"`
	ReplyTo     Address           `json:"reply_to,omitzero"`
	Subject     string            `json:"subject"`
	HTML        string            `json:"html,omitempty"`
	Text        string            `json:"text,omitempty"`
	Attachments []Attachment      `json:"attachments,omitempty"`
	Headers     map[string]string `json:"headers,omitempty"`
}

// Validate 检查邮件可以发送：至少一个收件人、主题非空且为单行、HTML 与 Text 至少一个、
// 地址合法，附件有文件名，头部名称和取值不含换行。From 为空时不检查，由驱动补上默认发件人。
func (m Message) Validate() error {
	var errs []error
	if len(m.To) == 0 {
		errs = append(errs, errors.Wrap(ErrInvalid, "at least one recipient is required"))
	}
	if strings.TrimSpace(m.Subject) == "" || strings.ContainsAny(m.Subject, "\r\n") {
		errs = append(errs, errors.Wrap(ErrInvalid, "subject must be a non-empty single line"))
	}
	if m.HTML == "" && m.Text == "" {
		errs = append(errs, errors.Wrap(ErrInvalid, "html or text body is required"))
	}
	addrs := []Address{}
	for _, a := range []Address{m.From, m.ReplyTo} {
		if !a.IsZero() {
			addrs = append(addrs, a)
		}
	}
	addrs = append(append(append(addrs, m.To...), m.Cc...), m.Bcc...)
	for _, a := range addrs {
		if err := a.Validate(); err != nil {
			errs = append(errs, err)
		}
	}
	for _, at := range m.Attachments {
		if strings.TrimSpace(at.Filename) == "" || strings.ContainsAny(at.Filename+at.ContentType, "\r\n") {
			errs = append(errs, errors.Wrapf(ErrInvalid, "attachment %q", at.Filename))
		}
	}
	for k, v := range m.Headers {
		if k == "" || strings.ContainsAny(k, " :\r\n") || strings.ContainsAny(v, "\r\n") {
			errs = append(errs, errors.Wrapf(ErrInvalid, "header %q", k))
		}
	}
	return errors.Join(errs...)
}

// WithDefaultFrom 在 From 为空时填入 from，供驱动实现使用。
func (m Message) WithDefaultFrom(from Address) Message {
	if m.From.IsZero() {
		m.From = from
	}
	return m
}
