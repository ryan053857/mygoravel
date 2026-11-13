// Package mimemsg 把 mail.Message 转成 go-mail 的消息，供 SMTP 和 SES 驱动共用。
package mimemsg

import (
	"bytes"

	gomail "github.com/wneessen/go-mail"

	"github.com/go-tenon/tenon/mail"
)

// Build 生成带 Message-ID 和 Date 的消息。Bcc 只记录为收件人，写出时不会出现在头部。
func Build(msg mail.Message) (*gomail.Msg, error) {
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
