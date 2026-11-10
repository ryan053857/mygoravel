package rest

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"strconv"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
	goi18n "github.com/nicksnyder/go-i18n/v2/i18n"

	"github.com/ryan053857/mygoravel/errorx"
	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/i18n"
)

// ErrorModel 在 Huma 原生 Problem Details 上增加业务错误码。
//
// 只有业务错误带 code。Huma 自身产生的校验和协议错误保持 huma.ErrorModel 原样，
// 它们同样是本类型 schema 的合法实例，因此 OpenAPI 统一用本类型描述错误响应。
type ErrorModel struct {
	huma.ErrorModel
	Code string `json:"code,omitempty" doc:"Stable business error code. Present only on business errors."`

	headers http.Header
}

// GetHeaders 实现 huma.HeadersError，返回随错误写出的响应头，例如 Retry-After。
func (e *ErrorModel) GetHeaders() http.Header { return e.headers }

// FromError 将处理器错误转换为 HTTP 错误，nil 输入返回 nil：
//   - 业务错误转为带 code 的 ErrorModel，detail 为 HTTP 状态描述，
//     带 errorx.RetryAfter 时设置 Retry-After 响应头（秒，向上取整）；
//   - 其他 huma.StatusError（例如 huma.Error404NotFound）即使被包装也原样返回；
//   - 其余错误转为通用 500，内部错误信息不会进入响应。
func FromError(err error) huma.StatusError {
	if err == nil {
		return nil
	}
	code := errorx.CodeOf(err)
	if code == "" {
		var se huma.StatusError
		if errors.As(err, &se) {
			return se
		}
		return huma.Error500InternalServerError("unexpected error occurred")
	}
	status := httpx.StatusOf(err)
	m := &ErrorModel{
		ErrorModel: huma.ErrorModel{Status: status, Title: http.StatusText(status), Detail: http.StatusText(status)},
		Code:       code,
	}
	if d := errorx.RetryAfterOf(err); d > 0 {
		m.headers = http.Header{"Retry-After": {strconv.Itoa(int(math.Ceil(d.Seconds())))}}
	}
	return m
}

// httpError 转换处理器错误。业务错误的错误码写入访问日志；context 带有 httpx.Locale
// 协商的语言时，以错误码为消息 ID 翻译 detail，errorx.Public 参数作为模板数据。
// 当前语言缺少翻译时使用默认语言的译文，都没有时保留 HTTP 状态描述；
// 两种情况都在访问日志中标记 untranslated。
func (a *API) httpError(ctx context.Context, err error) huma.StatusError {
	se := FromError(err)
	code := errorx.CodeOf(err)
	if code == "" {
		return se
	}
	httpx.AddAccessLogAttrs(ctx, slog.String("code", code))
	l := i18n.Localizer(ctx)
	if l == nil {
		return se
	}
	m := se.(*ErrorModel)
	msg, lerr := l.Localize(&goi18n.LocalizeConfig{MessageID: code, TemplateData: errorx.PublicOf(err)})
	if lerr != nil {
		httpx.AddAccessLogAttrs(ctx, slog.Bool("untranslated", true))
	}
	if msg != "" {
		m.Detail = msg
	}
	return se
}

// writeError 按 Huma 的方式在中间件中写出错误：协商内容类型、执行 Transformer 后序列化。
func writeError(api huma.API, ctx huma.Context, se huma.StatusError) {
	ct, err := api.Negotiate(ctx.Header("Accept"))
	if err != nil {
		ct = "application/json"
	}
	if f, ok := se.(huma.ContentTypeFilter); ok {
		ct = f.ContentType(ct)
	}
	if he, ok := se.(huma.HeadersError); ok {
		for k, values := range he.GetHeaders() {
			for _, v := range values {
				ctx.AppendHeader(k, v)
			}
		}
	}
	status := se.GetStatus()
	body, err := api.Transform(ctx, strconv.Itoa(status), se)
	if err != nil {
		body = se
	}
	ctx.SetHeader("Content-Type", ct)
	ctx.SetStatus(status)
	_ = api.Marshal(ctx.BodyWriter(), ct, body)
}
