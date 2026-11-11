package web_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/web"
)

// layout 渲染会话中的提示，模拟应用的布局。
var layout = templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
	var titles []string
	for _, m := range web.Flashes(ctx) {
		titles = append(titles, m.Type+":"+m.Title)
	}
	_, err := io.WriteString(w, "page["+strings.Join(titles, ",")+"]")
	return err
})

// flashApp 的路由：/save 存入提示后按 ?then= 结束响应，其余路径渲染 layout。
func flashApp() http.Handler {
	return handle(pages(), func(w http.ResponseWriter, r *http.Request) error {
		if r.URL.Path != "/save" {
			return web.Render(w, r, http.StatusOK, layout)
		}
		web.Flash(r.Context(), web.Message{Type: "success", Title: "已保存", Description: "「标题」"})
		switch r.URL.Query().Get("then") {
		case "redirect":
			web.Redirect(w, r, "/list")
		case "http-redirect":
			http.Redirect(w, r, "/list", http.StatusSeeOther)
		case "error":
			return web.ErrNotFound.New()
		case "no-content":
			w.WriteHeader(http.StatusNoContent)
		default:
			return web.Render(w, r, http.StatusOK, text("fragment"))
		}
		return nil
	})
}

// browser 依次发送请求并保留会话 cookie。
type browser struct {
	h      http.Handler
	cookie *http.Cookie
}

func (b *browser) do(r *http.Request) *httptest.ResponseRecorder {
	if b.cookie != nil {
		r.AddCookie(b.cookie)
	}
	rr := serve(b.h, r)
	for _, c := range rr.Result().Cookies() {
		b.cookie = c
	}
	return rr
}

// flashTrigger 解析 HX-Trigger 中的提示事件。
func flashTrigger(t *testing.T, rr *httptest.ResponseRecorder) []web.Message {
	t.Helper()
	raw := rr.Header().Get("HX-Trigger")
	if raw == "" {
		return nil
	}
	for _, r := range raw {
		if r > 127 {
			t.Fatalf("HX-Trigger is not ASCII: %s", raw)
		}
	}
	var events map[string]struct {
		Target   string        `json:"target"`
		Messages []web.Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		t.Fatalf("HX-Trigger %q: %v", raw, err)
	}
	ev, ok := events[web.FlashEvent]
	if ok && ev.Target != "body" {
		t.Errorf("flash event target = %q", ev.Target)
	}
	return ev.Messages
}

func TestFlashAfterRedirect(t *testing.T) {
	for _, then := range []string{"redirect", "http-redirect"} {
		for _, isHTMX := range []bool{false, true} {
			b := &browser{h: flashApp()}
			req := httptest.NewRequest("POST", "/save?then="+then, nil)
			if isHTMX {
				req = htmx(req, true)
			}
			rr := b.do(req)
			if msgs := flashTrigger(t, rr); msgs != nil {
				t.Errorf("%s htmx=%v: flash delivered with the redirect: %v", then, isHTMX, msgs)
			}
			if body := b.do(httptest.NewRequest("GET", "/list", nil)).Body.String(); body != "page[success:已保存]" {
				t.Errorf("%s htmx=%v: next page = %q", then, isHTMX, body)
			}
			if body := b.do(httptest.NewRequest("GET", "/list", nil)).Body.String(); body != "page[]" {
				t.Errorf("%s htmx=%v: flash shown twice: %q", then, isHTMX, body)
			}
		}
	}
}

func TestFlashWithHTMXResponse(t *testing.T) {
	for _, then := range []string{"render", "error", "no-content"} {
		b := &browser{h: flashApp()}
		rr := b.do(htmx(httptest.NewRequest("POST", "/save?then="+then, nil), true))
		msgs := flashTrigger(t, rr)
		want := web.Message{Type: "success", Title: "已保存", Description: "「标题」"}
		if len(msgs) != 1 || msgs[0] != want {
			t.Errorf("%s: delivered %v (status %d, HX-Trigger %q)", then, msgs, rr.Code, rr.Header().Get("HX-Trigger"))
		}
		// 已经送达，不会再出现在下一页。
		if body := b.do(httptest.NewRequest("GET", "/list", nil)).Body.String(); body != "page[]" {
			t.Errorf("%s: flash shown again: %q", then, body)
		}
	}
}

// 整页 htmx 请求（例如后退）由布局取出提示，响应头里不再重复。
func TestFlashFullHTMXPage(t *testing.T) {
	b := &browser{h: flashApp()}
	b.do(httptest.NewRequest("POST", "/save?then=redirect", nil))
	rr := b.do(htmx(httptest.NewRequest("GET", "/list", nil), false))
	if rr.Body.String() != "page[success:已保存]" || flashTrigger(t, rr) != nil {
		t.Errorf("body %q, HX-Trigger %q", rr.Body.String(), rr.Header().Get("HX-Trigger"))
	}
}

func TestFlashesWithoutSession(t *testing.T) {
	rr := serve(flashApp(), httptest.NewRequest("GET", "/", nil))
	if rr.Body.String() != "page[]" || len(rr.Result().Cookies()) > 0 {
		t.Errorf("anonymous page: %q cookies=%v", rr.Body.String(), rr.Result().Cookies())
	}
	if web.Flashes(context.Background()) != nil {
		t.Error("Flashes outside a page route should be nil")
	}
	defer func() {
		if recover() == nil {
			t.Error("Flash outside a page route did not panic")
		}
	}()
	web.Flash(context.Background(), web.Message{Title: "x"})
}

func TestTrigger(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("HX-Trigger", "a, b")
	web.Trigger(w, "article:saved", map[string]any{"title": "新标题 🎉"})
	web.Trigger(w, "list:changed", nil)
	raw := w.Header().Get("HX-Trigger")
	if strings.ContainsFunc(raw, func(r rune) bool { return r > 127 }) {
		t.Fatalf("not ASCII: %s", raw)
	}
	// 事件按写入顺序排列。
	var order []string
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.Token()
	for dec.More() {
		k, _ := dec.Token()
		order = append(order, k.(string))
		var skip json.RawMessage
		dec.Decode(&skip)
	}
	if strings.Join(order, " ") != "a b article:saved list:changed" {
		t.Errorf("order %v in %s", order, raw)
	}
	var events map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &events); err != nil {
		t.Fatal(err)
	}
	if e := events["article:saved"]; e["title"] != "新标题 🎉" || e["target"] != "body" {
		t.Errorf("article:saved = %v", e)
	}
	if e := events["list:changed"]; len(e) != 1 || e["target"] != "body" {
		t.Errorf("list:changed = %v", e)
	}
}
