package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ryan053857/mygoravel/poc/ui/components/toast"
	"github.com/ryan053857/mygoravel/web"
)

// flash 是「web.Flash」的原型：处理器只管 Put，下一次渲染（整页布局或局部响应）负责显示。
//
//   - 整页：布局调用 Flashes(ctx)，把 toast 标记渲染进 #toasts；
//   - htmx 局部响应：renderPartial 在响应末尾追加 <hx-partial hx-target="#toasts">；
//   - 跳转（303 或 HX-Redirect）：留在会话里，由跳转后的整页显示。

type flashKey struct{}

const sessionKey = "flash"

// withFlash 把会话管理器放进 context，供 putFlash / Flashes 使用。
func withFlash(sm *scs.SessionManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), flashKey{}, sm)))
		})
	}
}

func putFlash(ctx context.Context, t toast.Props) {
	sm := ctx.Value(flashKey{}).(*scs.SessionManager)
	var list []toast.Props
	if s := sm.GetString(ctx, sessionKey); s != "" {
		_ = json.Unmarshal([]byte(s), &list)
	}
	b, _ := json.Marshal(append(list, t))
	sm.Put(ctx, sessionKey, string(b))
}

func popFlashes(ctx context.Context) []toast.Props {
	sm, ok := ctx.Value(flashKey{}).(*scs.SessionManager)
	if !ok {
		return nil
	}
	// 没有会话 cookie 的请求不读写会话，避免公开页面产生 Set-Cookie。
	if !sm.Exists(ctx, sessionKey) {
		return nil
	}
	var list []toast.Props
	_ = json.Unmarshal([]byte(sm.PopString(ctx, sessionKey)), &list)
	return list
}

// Flashes 渲染并清空会话中的提示，放在布局里。
func Flashes() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		for _, t := range popFlashes(ctx) {
			if err := toast.Toast(t).Render(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}

// renderPartial 渲染 htmx 局部响应，并把待显示的提示作为 hx-partial 附在后面。
func renderPartial(w http.ResponseWriter, r *http.Request, status int, cs ...templ.Component) error {
	return web.Render(w, r, status, templ.ComponentFunc(func(ctx context.Context, wr io.Writer) error {
		for _, c := range cs {
			if err := c.Render(ctx, wr); err != nil {
				return err
			}
		}
		if list := popFlashes(ctx); len(list) > 0 {
			io.WriteString(wr, `<hx-partial hx-target="#toasts" hx-swap="beforeend">`)
			for _, t := range list {
				if err := toast.Toast(t).Render(ctx, wr); err != nil {
					return err
				}
			}
			io.WriteString(wr, `</hx-partial>`)
		}
		return nil
	}))
}

// trigger 设置 HX-Trigger 响应头：htmx 在交换完成后依次派发这些事件。
func trigger(w http.ResponseWriter, events map[string]any) {
	b, _ := json.Marshal(events)
	w.Header().Set("HX-Trigger", string(b))
}
