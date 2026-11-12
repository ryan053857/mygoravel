package webhook

import (
	"strings"
	"unicode"

	"github.com/cockroachdb/errors"
	"github.com/danielgtaylor/huma/v2"
)

// Document 把 events 写入 OpenAPI 文档的 webhooks，接收方可以据此生成事件内容的类型。
//
// 每种事件是一个 POST 请求：请求体为 {type, timestamp, data}，data 的 schema 由事件内容的类型生成，
// 命名规则与接口的请求和响应相同；请求头是 Standard Webhooks 的 webhook-id、webhook-timestamp 和
// webhook-signature；任何 2xx 响应表示收到。doc 通常是 rest.API 的 OpenAPI()：在注册接口的函数中调用，
// openapi 命令导出的文档就会包含这些事件。事件类型不合法或重复时 panic。
func Document(doc *huma.OpenAPI, events ...Documented) {
	if doc.Webhooks == nil {
		doc.Webhooks = map[string]*huma.PathItem{}
	}
	for _, ev := range events {
		d := ev.describe()
		if err := checkType(d.name); err != nil {
			panic(err)
		}
		if doc.Webhooks[d.name] != nil {
			panic(errors.Newf("webhook: event %q documented twice", d.name))
		}
		data := doc.Components.Schemas.Schema(d.data, true, hint(d.name)+"Data")
		doc.Webhooks[d.name] = &huma.PathItem{Post: &huma.Operation{
			Summary:    d.summary,
			Parameters: headers(),
			RequestBody: &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{
				"application/json": {Schema: &huma.Schema{
					Type:     huma.TypeObject,
					Required: []string{"type", "timestamp", "data"},
					Properties: map[string]*huma.Schema{
						"type":      {Type: huma.TypeString, Enum: []any{d.name}},
						"timestamp": {Type: huma.TypeString, Format: "date-time", Description: "When the event was sent."},
						"data":      data,
					},
				}},
			}},
			Responses: map[string]*huma.Response{
				"2XX": {Description: "Event received. Other statuses, timeouts and network errors are retried with backoff."},
			},
		}}
	}
}

// headers 返回 Standard Webhooks 的请求头。
func headers() []*huma.Param {
	return []*huma.Param{
		{Name: "webhook-id", In: "header", Required: true, Schema: &huma.Schema{Type: huma.TypeString},
			Description: "Event ID. Unchanged across retries; use it to deduplicate."},
		{Name: "webhook-timestamp", In: "header", Required: true, Schema: &huma.Schema{Type: huma.TypeString, Pattern: "^[0-9]+$"},
			Description: "Send time in Unix seconds."},
		{Name: "webhook-signature", In: "header", Required: true, Schema: &huma.Schema{Type: huma.TypeString},
			Description: "Space-separated v1,<base64 HMAC-SHA256 of id.timestamp.body>; more than one while a secret is being rotated."},
	}
}

// hint 把事件类型转换为匿名事件内容的 schema 名前缀，例如 article.published 为 ArticlePublished。
func hint(name string) string {
	var b strings.Builder
	for _, part := range strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		r := []rune(part)
		r[0] = unicode.ToUpper(r[0])
		b.WriteString(string(r))
	}
	return b.String()
}
