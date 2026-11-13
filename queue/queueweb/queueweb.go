// Package queueweb 是队列的运维面板：复制槽和延迟任务的状态、各 topic 的失败任务，以及重试和删除失败任务。
// 它只读写 queue 已有的数据，不新增存储：吞吐、耗时这类指标看 OpenTelemetry（Grafana），
// NSQ 各 topic 的积压和消费者看 nsqadmin，面板只放它们的链接。
//
// 面板是服务端渲染的页面，认证由应用决定，通常是 httpx.BasicAuth；它自己拒绝跨站的非安全请求，
// 因为浏览器会自动带上 Basic 凭据。应用还要挂载 web.Assets 和 ui.Assets：
//
//	router.Handle(web.AssetsPath+"*", web.Assets())
//	router.Handle(ui.AssetsPath+"*", ui.Assets())
//	ops := router.With(httpx.BasicAuth("ops", verifyOps))
//	queueweb.Register(ops, "/ops/queue", db, queueweb.Options{
//		Links: map[string]string{"Grafana": cfg.GrafanaURL, "nsqadmin": cfg.NSQAdminURL},
//	})
//
// 重试和删除各记一条 Info 日志，带上操作者（httpx.BasicUser）、topic 和条数。
package queueweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/ryan053857/mygoravel/httpx"
	"github.com/ryan053857/mygoravel/logging"
	"github.com/ryan053857/mygoravel/page"
	"github.com/ryan053857/mygoravel/queue"
	"github.com/ryan053857/mygoravel/web"
	"github.com/ryan053857/mygoravel/web/ui"
)

// bulkBatch 是全部重试、全部删除每批处理的条数。
const bulkBatch = 1000

// bulkDone 是全部重试、全部删除之后的提示（英文），译文在 i18n 中。
var bulkDone = map[string]string{"retried": "Retried {{.n}} failed jobs", "deleted": "Deleted {{.n}} failed jobs"}

// Options 是 Register 的选项，都可以为零值。
type Options struct {
	// Links 是页头右侧的外部链接，键是显示的名称，例如 Grafana、nsqadmin；地址为空的项不显示。
	Links map[string]string
	// Theme 是面板的外观，见 web.Theme。
	Theme web.Theme
	// Logger 记录重试、删除和 5xx 错误，nil 表示 slog.Default()。
	Logger *slog.Logger
}

type handlers struct {
	db     *bun.DB
	failed *queue.FailedJobs
	base   string
	links  []link
	logger *slog.Logger
}

type link struct{ name, url string }

// Register 把面板注册在 r 的 prefix 之下，例如 /ops/queue。r 通常已经装上了认证中间件。
func Register(r chi.Router, prefix string, db *bun.DB, opts Options) {
	if db == nil {
		panic("queueweb: nil database")
	}
	prefix = "/" + strings.Trim(prefix, "/")
	h := &handlers{
		db: db, failed: queue.NewFailedJobs(db), base: strings.TrimSuffix(prefix, "/"),
		logger: logging.Or(opts.Logger).With(logging.AttrComponent, "queueweb"),
	}
	for name, url := range opts.Links {
		if url != "" {
			h.links = append(h.links, link{name, url})
		}
	}
	sortLinks(h.links)
	pages := web.New(web.Options{ErrorPage: ui.ErrorPage, Theme: opts.Theme, Logger: opts.Logger})
	cop := http.NewCrossOriginProtection()
	r.Route(prefix, func(r chi.Router) {
		r.Use(cop.Handler)
		r.NotFound(pages.NotFound)
		rt := pages.Routes(r)
		rt.Get("/", h.overview)
		rt.Get("/failed", h.failedList)
		rt.Post("/failed/retry", h.retryAll)
		rt.Post("/failed/delete", h.deleteAll)
		rt.Get("/failed/{id}", h.failedShow)
		rt.Post("/failed/{id}/retry", h.retry)
		rt.Post("/failed/{id}/delete", h.delete)
		rt.Get("/scheduled", h.scheduled)
	})
}

func (h *handlers) overview(w http.ResponseWriter, r *http.Request) error {
	st, err := queue.ReadStatus(r.Context(), h.db)
	if err != nil {
		return err
	}
	counts, err := h.failed.Counts(r.Context())
	if err != nil {
		return err
	}
	v := overviewView{base: h.base, status: st, counts: counts}
	if web.IsPartial(r) {
		return web.Render(w, r, http.StatusOK, overviewSection(v))
	}
	return web.Render(w, r, http.StatusOK, layout(h.frame(navOverview), overviewSection(v)))
}

// FailedQuery 是失败任务列表的查询参数。
type FailedQuery struct {
	Topic string `query:"topic" json:"topic,omitempty"`
	page.OffsetParams
}

func (h *handlers) failedList(w http.ResponseWriter, r *http.Request) error {
	q, err := web.ParseQuery[FailedQuery](r)
	if err != nil {
		return err
	}
	return h.renderFailed(w, r, q, "")
}

// renderFailed 渲染失败任务列表；notice 不为空时在列表上方显示。局部请求只渲染列表区域。
func (h *handlers) renderFailed(w http.ResponseWriter, r *http.Request, q FailedQuery, notice string) error {
	ctx := r.Context()
	counts, err := h.failed.Counts(ctx)
	if err != nil {
		return err
	}
	var total int64
	for _, c := range counts {
		if q.Topic == "" || c.Topic == q.Topic {
			total += int64(c.Count)
		}
	}
	jobs, err := h.failed.List(ctx, queue.FailedQuery{Topic: q.Topic, Limit: q.PerPage, Offset: int(q.Offset())})
	if err != nil {
		return err
	}
	v := failedView{base: h.base, query: q, counts: counts, list: page.NewOffset(jobs, q.OffsetParams, total), notice: notice}
	if web.IsPartial(r) {
		web.PushURL(w, v.url(q))
		return web.Render(w, r, http.StatusOK, failedSection(v))
	}
	return web.Render(w, r, http.StatusOK, layout(h.frame(navFailed), failedSection(v)))
}

func (h *handlers) failedShow(w http.ResponseWriter, r *http.Request) error {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return web.ErrNotFound.New()
	}
	job, err := h.failed.Get(r.Context(), id)
	if err != nil {
		return notFound(err)
	}
	return web.Render(w, r, http.StatusOK, layout(h.frame(navFailed), jobDetail(h.base, job, pretty(job.Payload))))
}

func (h *handlers) retry(w http.ResponseWriter, r *http.Request) error {
	return h.single(w, r, "retried", h.failed.Retry)
}

func (h *handlers) delete(w http.ResponseWriter, r *http.Request) error {
	return h.single(w, r, "deleted", h.failed.Delete)
}

// single 对一条失败任务执行 do，记日志后回到它所在 topic 的列表。
func (h *handlers) single(w http.ResponseWriter, r *http.Request, action string, do func(ctx context.Context, id uuid.UUID) error) error {
	ctx := r.Context()
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return web.ErrNotFound.New()
	}
	job, err := h.failed.Get(ctx, id)
	if err != nil {
		return notFound(err)
	}
	if err := do(ctx, id); err != nil {
		return notFound(err)
	}
	h.logger.InfoContext(ctx, "failed job "+action, "user", httpx.BasicUser(ctx), "topic", job.Topic, "id", id, "count", 1)
	web.Redirect(w, r, failedView{base: h.base}.url(FailedQuery{Topic: job.Topic}))
	return nil
}

func (h *handlers) retryAll(w http.ResponseWriter, r *http.Request) error {
	return h.bulk(w, r, "retried", h.failed.RetryAll)
}

func (h *handlers) deleteAll(w http.ResponseWriter, r *http.Request) error {
	return h.bulk(w, r, "deleted", h.failed.DeleteAll)
}

// bulk 分批处理 topic（查询参数，为空时是全部 topic）中点击之前失败的任务，直到处理完，再渲染列表并显示条数。
// 只处理点击之前失败的任务：重试之后马上又失败的任务不会在这一轮中被反复放回。
func (h *handlers) bulk(w http.ResponseWriter, r *http.Request, action string, do func(ctx context.Context, topic string, before time.Time, limit int) (int, error)) error {
	ctx := r.Context()
	topic := r.URL.Query().Get("topic")
	before := time.Now()
	total := 0
	for {
		n, err := do(ctx, topic, before, bulkBatch)
		total += n
		if err != nil {
			return err
		}
		if n < bulkBatch {
			break
		}
	}
	h.logger.InfoContext(ctx, "failed jobs "+action, "user", httpx.BasicUser(ctx), "topic", topic, "count", total)
	q := FailedQuery{Topic: topic, OffsetParams: page.OffsetParams{Page: 1, PerPage: defaultPerPage}}
	return h.renderFailed(w, r, q, t(ctx, "queueweb."+action, bulkDone[action], map[string]any{"n": total}))
}

// ScheduledQuery 是延迟任务列表的查询参数。
type ScheduledQuery struct {
	page.OffsetParams
}

func (h *handlers) scheduled(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	q, err := web.ParseQuery[ScheduledQuery](r)
	if err != nil {
		return err
	}
	st, err := queue.ReadStatus(ctx, h.db)
	if err != nil {
		return err
	}
	jobs, err := queue.ListScheduled(ctx, h.db, queue.ScheduledQuery{Limit: q.PerPage, Offset: int(q.Offset())})
	if err != nil {
		return err
	}
	v := scheduledView{base: h.base, list: page.NewOffset(jobs, q.OffsetParams, int64(st.Scheduled))}
	return web.Render(w, r, http.StatusOK, layout(h.frame(navScheduled), scheduledSection(v)))
}

func (h *handlers) frame(current nav) frame {
	return frame{base: h.base, current: current, links: h.links}
}

// notFound 把失败任务不存在转换为 404 页面。
func notFound(err error) error {
	if errors.Is(err, queue.ErrFailedJobNotFound) {
		return web.ErrNotFound.Wrap(err)
	}
	return err
}

// pretty 缩进 JSON；不是 JSON 时原样返回。
func pretty(raw []byte) string {
	var b bytes.Buffer
	if json.Indent(&b, raw, "", "  ") != nil {
		return string(raw)
	}
	return b.String()
}
