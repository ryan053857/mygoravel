package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/alexedwards/scs/v2"
)

// Message 是一条一次性提示，例如「文章已保存」。字段的 JSON 名与常见 toast 组件的参数一致。
type Message struct {
	// Type 是提示的种类，惯例为 success、info、warning、error，由应用的组件解释。
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// FlashEvent 是 htmx 请求收到提示时，在 body 上派发的事件名；event.detail.messages 是 []Message。
const FlashEvent = "web:flash"

const flashKey = "web.flash"

type sessionsKey struct{}

// Flash 把 m 存入会话，由之后的响应显示一次，适合「保存后跳转，在下一页提示」：
//
//   - 整页：布局调用 Flashes 取出并渲染；
//   - htmx 请求的非跳转响应：随响应送达，htmx 在 body 上派发 FlashEvent；
//   - 跳转（303 或 HX-Redirect）：留在会话里，由跳转后的页面显示。
//
// 只能在页面路由中、写出响应之前调用，否则 panic。
func Flash(ctx context.Context, m Message) {
	sm := sessionsFrom(ctx)
	if sm == nil {
		panic("web: Flash called outside a page route")
	}
	b, err := json.Marshal(append(peekFlashes(ctx, sm), m))
	if err != nil {
		panic(err) // Message 总能编码
	}
	sm.Put(ctx, flashKey, string(b))
}

// Flashes 取出并清空会话中的提示，在布局中渲染：
//
//	for _, m := range web.Flashes(ctx) { … }
//
// 没有提示或不在页面路由中时返回 nil；没有会话的请求不会因此创建会话。
func Flashes(ctx context.Context) []Message {
	sm := sessionsFrom(ctx)
	if sm == nil {
		return nil
	}
	list := peekFlashes(ctx, sm)
	if list != nil {
		sm.Remove(ctx, flashKey)
	}
	return list
}

func sessionsFrom(ctx context.Context) *scs.SessionManager {
	sm, _ := ctx.Value(sessionsKey{}).(*scs.SessionManager)
	return sm
}

func peekFlashes(ctx context.Context, sm *scs.SessionManager) []Message {
	if !sm.Exists(ctx, flashKey) {
		return nil
	}
	var list []Message
	if err := json.Unmarshal([]byte(sm.GetString(ctx, flashKey)), &list); err != nil {
		return nil
	}
	return list
}

// Trigger 让 htmx 在处理完这个响应后，在 body 上派发事件 name，detail 的各项成为 event.detail 的字段。
// 可以多次调用，事件按调用顺序派发；必须在写出响应之前调用。
//
//	web.Trigger(w, "article:saved", map[string]any{"id": a.ID})
//
// 页面上用 hx-trigger="article:saved from:body" 或 document 上的监听器响应它。
// 事件派发在 body 而不是发起请求的元素上，因此该元素在这次替换中被移除时，事件依然送达。
func Trigger(w http.ResponseWriter, name string, detail map[string]any) {
	d := make(map[string]any, len(detail)+1)
	for k, v := range detail {
		d[k] = v
	}
	d["target"] = "body" // htmx 在 target 选择的元素上派发
	b, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("web: Trigger %s: %v", name, err))
	}
	key, _ := json.Marshal(name)
	h := w.Header()
	// HX-Trigger 是一个 JSON 对象：追加一项，保持派发顺序。已有的逗号分隔写法先转换为对象。
	cur := strings.TrimSpace(h.Get("HX-Trigger"))
	switch {
	case cur == "":
		cur = "{}"
	case !strings.HasPrefix(cur, "{"):
		var obj strings.Builder
		for _, n := range strings.Split(cur, ",") {
			if n = strings.TrimSpace(n); n != "" {
				k, _ := json.Marshal(n)
				obj.WriteString("," + string(k) + ":{}")
			}
		}
		cur = "{" + strings.TrimPrefix(obj.String(), ",") + "}"
	}
	sep := ","
	if cur == "{}" {
		sep = ""
	}
	h.Set("HX-Trigger", asciiJSON(strings.TrimSuffix(cur, "}")+sep+string(key)+":"+string(b)+"}"))
}

// DialogCloseEvent 是 CloseDialog 派发的事件，event.detail.id 是对话框的 ID。web/ui 的衔接脚本把它交给对话框组件。
const DialogCloseEvent = "web:dialog-close"

// CloseDialog 让 htmx 处理完这个响应后关闭 id 对应的对话框，例如对话框中的表单保存成功后。
// 必须在写出响应之前调用。
func CloseDialog(w http.ResponseWriter, id string) {
	Trigger(w, DialogCloseEvent, map[string]any{"id": id})
}

// asciiJSON 把 JSON 中的非 ASCII 字符转义为 \uXXXX：浏览器按 Latin-1 解读响应头，中文必须转义。
func asciiJSON(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r < utf8.RuneSelf:
			b.WriteRune(r)
		case r > 0xFFFF:
			r -= 0x10000
			fmt.Fprintf(&b, `\u%04x\u%04x`, 0xD800+(r>>10), 0xDC00+(r&0x3FF))
		default:
			fmt.Fprintf(&b, `\u%04x`, r)
		}
	}
	return b.String()
}

// deliverFlashes 在 htmx 请求的非跳转响应写出状态码之前调用：把会话中的提示和 extra 放进 HX-Trigger。
func deliverFlashes(w http.ResponseWriter, r *http.Request, status int, extra []Message) {
	h := w.Header()
	if !IsHTMX(r) || (status >= 300 && status < 400) || h.Get("HX-Redirect") != "" || h.Get("HX-Location") != "" || h.Get("HX-Refresh") == "true" {
		return
	}
	if list := append(Flashes(r.Context()), extra...); len(list) > 0 {
		Trigger(w, FlashEvent, map[string]any{"messages": list})
	}
}
