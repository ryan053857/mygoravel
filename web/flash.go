package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Message 是一条提示，例如局部请求的错误。字段的 JSON 名与常见 toast 组件的参数一致。
type Message struct {
	// Type 是提示的种类，惯例为 success、info、warning、error，由应用的组件解释。
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
}

// FlashEvent 是局部 htmx 请求出错时（没有设置 Options.ErrorFragment），在 body 上派发的事件名；
// event.detail.messages 是 []Message。ui.Toaster 监听它，把错误显示为 toast。
const FlashEvent = "web:flash"

// trigger 让 htmx 在处理完这个响应后，在 body 上派发事件 name，detail 的各项成为 event.detail 的字段。
// 事件派发在 body 而不是发起请求的元素上，因此该元素在这次替换中被移除时，事件依然送达。
func trigger(w http.ResponseWriter, name string, detail map[string]any) {
	d := make(map[string]any, len(detail)+1)
	for k, v := range detail {
		d[k] = v
	}
	d["target"] = "body" // htmx 在 target 选择的元素上派发
	b, err := json.Marshal(d)
	if err != nil {
		panic(fmt.Sprintf("web: trigger %s: %v", name, err))
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

// deliverMessages 在 htmx 请求的非跳转响应写出状态码之前调用：把 messages 放进 HX-Trigger。
func deliverMessages(w http.ResponseWriter, r *http.Request, status int, messages []Message) {
	h := w.Header()
	if len(messages) == 0 || !IsHTMX(r) || (status >= 300 && status < 400) || h.Get("HX-Redirect") != "" || h.Get("HX-Location") != "" || h.Get("HX-Refresh") == "true" {
		return
	}
	trigger(w, FlashEvent, map[string]any{"messages": messages})
}
