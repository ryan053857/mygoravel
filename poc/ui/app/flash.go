package app

import (
	"context"
	"io"

	"github.com/a-h/templ"

	"github.com/ryan053857/mygoravel/poc/ui/components/toast"
	"github.com/ryan053857/mygoravel/web"
)

// Flashes 把会话中的提示渲染为 toast 标记，放在布局里（web.Flashes 取出并清空）。
func Flashes() templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		for _, m := range web.Flashes(ctx) {
			t := toast.Props{Title: m.Title, Description: m.Description, Type: toast.Type(m.Type)}
			if err := toast.Toast(t).Render(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}
