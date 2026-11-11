package app

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/identity"
	"github.com/ryan053857/mygoravel/page"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/app/article"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/app/views"
	"github.com/ryan053857/mygoravel/poc/uibuiltin/webui"
	"github.com/ryan053857/mygoravel/web"
)

type opsHandlers struct {
	pages *web.Pages
	store *article.Store
}

func (h *opsHandlers) loginPage(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, views.OpsLogin(web.NewForm(views.LoginForm{})))
}

func (h *opsHandlers) login(w http.ResponseWriter, r *http.Request) error {
	f, err := web.ParseForm[views.LoginForm](r)
	if err != nil {
		return err
	}
	if f.Valid() && f.Data.Email == "ops@example.com" && f.Data.Password == "secret" {
		if err := h.pages.Login(r, identity.Subject{ID: "ops", Type: identity.SubjectUser}); err != nil {
			return err
		}
		web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/ops/articles"))
		return nil
	}
	if f.Valid() {
		f.SetError("password", "邮箱或密码不正确")
	}
	return web.Render(w, r, http.StatusUnprocessableEntity, views.OpsLogin(f))
}

func (h *opsHandlers) logout(w http.ResponseWriter, r *http.Request) error {
	if err := h.pages.Logout(r.Context()); err != nil {
		return err
	}
	web.Redirect(w, r, "/ops/login")
	return nil
}

func (h *opsHandlers) list(w http.ResponseWriter, r *http.Request) error {
	q, err := web.ParseQuery[views.ListQuery](r)
	if err != nil {
		return err
	}
	items, total := h.store.List(article.Query{Q: q.Q, Status: q.Status, Page: q.Page, PerPage: q.PerPage})
	list := page.NewOffset(items, q.OffsetParams, total)
	if web.IsPartial(r) {
		web.PushURL(w, q.URL())
		return web.Render(w, r, http.StatusOK, views.OpsArticleTable(q, list))
	}
	return web.Render(w, r, http.StatusOK, views.OpsArticles(q, list))
}

func (h *opsHandlers) newDialog(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, views.OpsNewDialog(web.NewForm(article.Changes{Status: "draft"})))
}

func (h *opsHandlers) create(w http.ResponseWriter, r *http.Request) error {
	f, err := web.ParseForm[article.Changes](r)
	if err != nil {
		return err
	}
	if f.Valid() {
		_, err := h.store.Create(f.Data)
		if err == nil {
			webui.CloseDialog(w, "article-dialog")
			web.Trigger(w, "articles:changed", nil)
			web.Flash(r.Context(), web.Message{Type: "success", Title: "文章已创建"})
			w.WriteHeader(http.StatusNoContent)
			return nil
		}
		if !errors.Is(err, article.ErrSlugTaken) {
			return err
		}
		f.SetError("slug", httpx.ErrorDetail(r.Context(), err))
	}
	return web.Render(w, r, http.StatusUnprocessableEntity, views.OpsArticleForm(f))
}

func (h *opsHandlers) find(r *http.Request) (article.Article, error) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	a, ok := h.store.Get(id)
	if !ok {
		return a, article.ErrNotFound.New()
	}
	return a, nil
}

func (h *opsHandlers) deleteDialog(w http.ResponseWriter, r *http.Request) error {
	a, err := h.find(r)
	if err != nil {
		return err
	}
	return web.Render(w, r, http.StatusOK, views.OpsDeleteDialog(a))
}

func (h *opsHandlers) delete(w http.ResponseWriter, r *http.Request) error {
	a, err := h.find(r)
	if err != nil {
		return err
	}
	h.store.Delete(a.ID)
	webui.CloseDialog(w, "delete-dialog")
	web.Trigger(w, "articles:changed", nil)
	web.Flash(r.Context(), web.Message{Type: "success", Title: "已删除「" + a.Title + "」"})
	w.WriteHeader(http.StatusNoContent)
	return nil
}

type tenantHandlers struct {
	pages *web.Pages
	store *article.Store
}

func (h *tenantHandlers) loginPage(w http.ResponseWriter, r *http.Request) error {
	return web.Render(w, r, http.StatusOK, views.TenantLogin(web.NewForm(views.LoginForm{})))
}

func (h *tenantHandlers) login(w http.ResponseWriter, r *http.Request) error {
	f, err := web.ParseForm[views.LoginForm](r)
	if err != nil {
		return err
	}
	if f.Valid() && f.Data.Email == "alice@acme.test" && f.Data.Password == "secret" {
		if err := h.pages.Login(r, identity.Subject{ID: "alice", Type: identity.SubjectUser}); err != nil {
			return err
		}
		web.Flash(r.Context(), web.Message{Type: "success", Title: "欢迎回来，Alice"})
		web.Redirect(w, r, web.LocalPath(r.URL.Query().Get("next"), "/app/"))
		return nil
	}
	if f.Valid() {
		f.SetError("password", "邮箱或密码不正确")
	}
	return web.Render(w, r, http.StatusUnprocessableEntity, views.TenantLogin(f))
}

func (h *tenantHandlers) logout(w http.ResponseWriter, r *http.Request) error {
	if err := h.pages.Logout(r.Context()); err != nil {
		return err
	}
	web.Redirect(w, r, "/app/login")
	return nil
}

func (h *tenantHandlers) home(w http.ResponseWriter, r *http.Request) error {
	items, _ := h.store.List(article.Query{Page: 1, PerPage: 6})
	return web.Render(w, r, http.StatusOK, views.TenantHome(items))
}

func (h *tenantHandlers) star(w http.ResponseWriter, r *http.Request) error {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	a, ok := h.store.Get(id)
	if !ok {
		return article.ErrNotFound.New()
	}
	web.Flash(r.Context(), web.Message{Type: "success", Title: "已收藏「" + a.Title + "」"})
	w.WriteHeader(http.StatusNoContent)
	return nil
}
