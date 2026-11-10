package httpx

import (
	"context"
	"log/slog"
	"net/http"

	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/i18n"
)

// ErrorDetail 返回面向用户的错误描述。
//
// 业务错误以错误码为消息 ID，按 Locale 协商的语言翻译，errorx.Public 参数作为模板数据；
// 当前语言缺少译文时使用默认语言，都没有时返回 StatusOf(err) 的状态描述。
// 其他错误一律返回状态描述，内部错误信息不会出现在结果中。
//
// 业务错误码会写入访问日志的 code 字段；有 Locale 但缺少译文时另外标记 untranslated。
func ErrorDetail(ctx context.Context, err error) string {
	fallback := http.StatusText(StatusOf(err))
	code := errorx.CodeOf(err)
	if code == "" {
		return fallback
	}
	AddAccessLogAttrs(ctx, slog.String("code", code))
	l := i18n.Localizer(ctx)
	if l == nil {
		return fallback
	}
	msg, lerr := l.Localize(&goi18n.LocalizeConfig{MessageID: code, TemplateData: errorx.PublicOf(err)})
	if lerr != nil {
		AddAccessLogAttrs(ctx, slog.Bool("untranslated", true))
	}
	if msg == "" {
		return fallback
	}
	return msg
}
