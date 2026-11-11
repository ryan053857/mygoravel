// Package app 是 PoC 的演示应用：后台文章管理（shadcn-templ 组件）和落地页（设计令牌 + Tailwind）。
package app

import (
	"errors"
	"io/fs"
	"net/http"
	"strconv"

	"github.com/alexedwards/scs/v2/memstore"
	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/poc/ui/app/article"
	"github.com/ryan053857/mygoravel/poc/ui/app/views"
	"github.com/ryan053857/mygoravel/poc/ui/components/toast"
	"github.com/ryan053857/mygoravel/poc/ui/static"
	"github.com/ryan053857/mygoravel/session"
	"github.com/ryan053857/mygoravel/web"
)

// New 组装演示应用。assets 是 assets 目录（css/app.css 与 js/ 下的组件脚本）。
func New(assets fs.FS, dev bool) (http.Handler, error) {
	st, err := static.New("/assets/", assets, dev)
	if err != nil {
		return nil, err
	}
	var cfg session.Config
	cfg.SetDefaults()
	cfg.Cookie.Secure = false
	sm := session.New(cfg, memstore.New())
	pages := web.New(web.Options{
		Sessions:      sm,
		Login:         "/login",
		ErrorPage:     views.ErrorPage,
		ErrorFragment: views.ErrorFragment,
		ErrorTarget:   "#flash",
	})
	h := &handlers{store: article.NewStore()}

	r := chi.NewRouter()
	r.Use(httpx.Locale(nil), st.Middleware, withFlash(sm))
	r.Handle("/assets/*", st)
	r.Handle(web.AssetsPath+"*", web.Assets())
	r.NotFound(pages.NotFound)

	public := pages.Routes(r, web.Public)
	public.Get("/", h.landing)
	public.Get("/pricing", h.pricing)
	public.Post("/subscribe", h.subscribe)
	// PoC 不做登录，后台也是公开路由。
	public.Get("/admin/articles", h.list)
	public.Get("/admin/articles/new", h.newForm)
	public.Post("/admin/articles", h.create)
	public.Get("/admin/articles/{id}/edit", h.editForm)
	public.Put("/admin/articles/{id}", h.update)
	public.Get("/admin/articles/{id}/delete", h.confirmDelete)
	public.Delete("/admin/articles/{id}", h.delete)
	return r, nil
}

type handlers struct{ store *article.Store }

// ---- 落地页 ----

func (h *handlers) landing(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, views.Landing(false, web.NewForm(views.Subscribe{}), Flashes()))
}

func (h *handlers) pricing(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, views.Pricing(r.URL.Query().Get("period") == "yearly"))
}

func (h *handlers) subscribe(w http.ResponseWriter, r *http.Request) error {
	f, err := web.ParseForm[views.Subscribe](r)
	if err != nil {
		return err
	}
	if !f.Valid() {
		return web.Render(w, r, http.StatusUnprocessableEntity, views.Landing(false, f, Flashes()))
	}
	putFlash(r.Context(), toast.Props{Title: "订阅成功", Description: f.Data.Email + " 将收到我们的月刊。", Type: toast.TypeSuccess})
	web.Redirect(w, r, "/#subscribe")
	return nil
}

// ---- 后台：列表 ----

func listState(r *http.Request) views.ListState {
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	return views.ListState{Q: q.Get("q"), Status: q.Get("status"), Sort: q.Get("sort"), Page: max(page, 1), PerPage: 10}
}

func (h *handlers) list(w http.ResponseWriter, r *http.Request) error {
	s := listState(r)
	rows, total := h.store.List(article.Query{Q: s.Q, Status: s.Status, Sort: s.Sort, Page: s.Page, PerPage: s.PerPage})
	s.Total = total
	if s.Page > s.Pages() { // 删除最后一页的最后一条后回到新的最后一页
		s.Page = s.Pages()
		rows, _ = h.store.List(article.Query{Q: s.Q, Status: s.Status, Sort: s.Sort, Page: s.Page, PerPage: s.PerPage})
	}
	if web.IsPartial(r) {
		// 表单 GET 会把空字段也写进地址（?q=&status=），由服务端给出规范地址。
		w.Header().Set("HX-Push-Url", s.URL(func(*views.ListState) {}))
		return renderPartial(w, r, http.StatusOK, views.ArticlesTable(s, rows), views.ListSync(s))
	}
	return web.Render(w, r, http.StatusOK, views.ArticlesPage(s, rows, Flashes()))
}

// ---- 后台：新建与编辑（对话框） ----

func (h *handlers) newForm(w http.ResponseWriter, r *http.Request) error {
	return renderPartial(w, r, http.StatusOK, views.ArticleDialog(0, web.NewForm(article.Changes{Status: "draft"})))
}

func (h *handlers) editForm(w http.ResponseWriter, r *http.Request) error {
	a, err := h.find(r)
	if err != nil {
		return err
	}
	return renderPartial(w, r, http.StatusOK, views.ArticleDialog(a.ID, web.NewForm(article.Changes{Title: a.Title, Slug: a.Slug, Status: a.Status})))
}

func (h *handlers) create(w http.ResponseWriter, r *http.Request) error {
	return h.save(w, r, 0)
}

func (h *handlers) update(w http.ResponseWriter, r *http.Request) error {
	a, err := h.find(r)
	if err != nil {
		return err
	}
	return h.save(w, r, a.ID)
}

// save 是新建和编辑共用的提交流程：
//   - 验证失败：422，重新渲染表单（hx-swap=outerMorph 只替换表单，对话框保持打开）；
//   - 成功：HX-Reswap: none 不替换表单，HX-Trigger 关闭对话框并让列表按当前状态刷新，toast 提示。
func (h *handlers) save(w http.ResponseWriter, r *http.Request, id int) error {
	f, err := web.ParseForm[article.Changes](r)
	if err != nil {
		return err
	}
	if f.Valid() {
		var a article.Article
		if id == 0 {
			a, err = h.store.Create(f.Data)
		} else {
			a, err = h.store.Update(id, f.Data)
		}
		switch {
		case errors.Is(err, article.ErrSlugTaken):
			f.SetError("slug", "这个 slug 已被使用")
		case err != nil:
			return err
		default:
			w.Header().Set("HX-Reswap", "none")
			trigger(w, map[string]any{"dialog:close": map[string]string{"id": "article-dialog"}, "articles:changed": true})
			putFlash(r.Context(), toast.Props{Title: "已保存", Description: "「" + a.Title + "」", Type: toast.TypeSuccess})
			return renderPartial(w, r, http.StatusOK)
		}
	}
	return renderPartial(w, r, http.StatusUnprocessableEntity, views.ArticleForm(id, f))
}

// ---- 后台：删除 ----

func (h *handlers) confirmDelete(w http.ResponseWriter, r *http.Request) error {
	a, err := h.find(r)
	if err != nil {
		return err
	}
	return renderPartial(w, r, http.StatusOK, views.DeleteDialog(a))
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) error {
	a, err := h.find(r)
	if err != nil {
		return err
	}
	h.store.Delete(a.ID)
	trigger(w, map[string]any{"dialog:close": map[string]string{"id": "delete-dialog"}, "articles:changed": true})
	putFlash(r.Context(), toast.Props{Title: "已删除", Description: "「" + a.Title + "」", Type: toast.TypeSuccess})
	return renderPartial(w, r, http.StatusOK)
}

func (h *handlers) find(r *http.Request) (article.Article, error) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	a, ok := h.store.Get(id)
	if !ok {
		return a, article.ErrNotFound.New()
	}
	return a, nil
}
