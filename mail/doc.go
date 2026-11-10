// Package mail 定义邮件消息与发送接口，并用 html/template 渲染邮件模板。
//
//	tpl, err := mail.NewTemplates(templatesFS)
//	msg, err := tpl.Render(ctx, "welcome", data)
//	msg.To = []mail.Address{{Email: "ada@example.com"}}
//	err = mailer.Send(ctx, msg)
//
// 发送由驱动完成：mail/smtp、mail/resend 用于生产，mail/logmail 在开发时把邮件写进日志，
// mail/mailtest 在测试中记录发出的邮件。Send 是同步的；Message 可以序列化为 JSON，以便交给队列异步发送。
package mail
