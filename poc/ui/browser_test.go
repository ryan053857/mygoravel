package ui_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"

	ui "github.com/ryan053857/mygoravel/poc/ui"
	"github.com/ryan053857/mygoravel/poc/ui/app"
)

// 浏览器场景需要本机 Chrome：POC_BROWSER=1 go test -run TestBrowser -v .
// SHOTS=dir 时把关键画面截图保存到 dir。

func start(t *testing.T) string {
	t.Helper()
	h, err := app.New(ui.Assets, false)
	if err != nil {
		t.Fatal(err)
	}
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func tab(t *testing.T) context.Context {
	t.Helper()
	if os.Getenv("POC_BROWSER") == "" {
		t.Skip("set POC_BROWSER=1 to run browser scenarios")
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath("/usr/bin/google-chrome"), chromedp.Flag("lang", "zh-CN"), chromedp.Flag("accept-lang", "zh-CN"))...)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(func() { cancel(); cancelAlloc() })
	// 第一次运行会启动浏览器，必须用不会被取消的根 context。
	if err := chromedp.Do(ctx, chromedp.Navigate("about:blank"), chromedp.EmulateViewport(1280, 860)); err != nil {
		t.Fatal(err)
	}
	return ctx
}

func do(t *testing.T, ctx context.Context, steps ...chromedp.Action[chromedp.Void]) {
	t.Helper()
	c, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := chromedp.Do(c, steps...); err != nil {
		t.Fatal(err)
	}
}

func js[T any](t *testing.T, ctx context.Context, expr string) T {
	t.Helper()
	v, err := chromedp.Run(ctx, chromedp.Evaluate[T](expr))
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	return v
}

func waitFor(t *testing.T, ctx context.Context, expr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expr)); err == nil && ok {
			return
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s (location %s)", expr, js[string](t, ctx, "location.href"))
}

func shot(t *testing.T, ctx context.Context, name string) {
	dir := os.Getenv("SHOTS")
	if dir == "" {
		return
	}
	time.Sleep(400 * time.Millisecond) // 等动画结束
	b, err := chromedp.Run(ctx, chromedp.CaptureScreenshot())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".png"), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// 安装错误收集：页面上任何未捕获异常记入 window.__errors。
const trapErrors = `window.__errors = []; addEventListener("error", e => { if (!String(e.message).startsWith("ResizeObserver loop")) __errors.push(String(e.message)) }); addEventListener("unhandledrejection", e => __errors.push(String(e.reason))); true`

func noErrors(t *testing.T, ctx context.Context) {
	t.Helper()
	if errs := js[[]string](t, ctx, `window.__errors || []`); len(errs) > 0 {
		t.Errorf("page errors: %v", errs)
	}
}

func open(t *testing.T, ctx context.Context, url string) {
	t.Helper()
	do(t, ctx, chromedp.Navigate(url))
	waitFor(t, ctx, `document.readyState === "complete" && !!window.templ && !!window.htmx`)
	js[bool](t, ctx, trapErrors)
}

const dialogOpen = `(id => !!window.templ.dialog.isOpen(id))`

// dialogGone：对话框已关闭且关闭动画结束（遮罩不再可见），之后的点击才不会落在遮罩上。
const dialogGone = `(id => !window.templ.dialog.isOpen(id) && ![...document.querySelectorAll("[data-slot$=dialog-overlay]")].some(e => e.checkVisibility()))`
const toastWith = `(s => [...document.querySelectorAll('[data-slot=toast]')].some(el => el.textContent.includes(s)))`
const total = `document.querySelector("#total").textContent.trim()`

// L1：筛选、排序、翻页只替换列表区域并同步地址栏；后退恢复上一个状态。
func TestBrowserList(t *testing.T) {
	ctx := tab(t)
	base := start(t)
	open(t, ctx, base+"/admin/articles")
	if got := js[string](t, ctx, total); got != "共 57 篇" {
		t.Fatalf("total = %q", got)
	}
	shot(t, ctx, "admin-list")
	js[bool](t, ctx, `document.body.dataset.marker = "kept"; true`) // 整页刷新会丢失这个标记

	do(t, ctx, chromedp.SendKeys("#q", "htmx"))
	waitFor(t, ctx, `location.search === "?q=htmx" && `+total+` === "共 5 篇"`)

	do(t, ctx, chromedp.Click(`[data-sort=views]`))
	waitFor(t, ctx, `location.search === "?q=htmx&sort=views" && document.querySelector("#sort-input").value === "views"`)
	// 排序之后继续筛选，排序保持不变。
	do(t, ctx, chromedp.SendKeys("#q", " 实践"))
	waitFor(t, ctx, `location.search.includes("sort=views") && location.search.includes("q=htmx")`)
	views := js[[]string](t, ctx, `[...document.querySelectorAll("#articles tbody tr td:nth-child(3)")].map(td => td.textContent.trim())`)
	t.Logf("sorted views: %v", views)

	do(t, ctx, chromedp.Navigate(base+"/admin/articles"))
	waitFor(t, ctx, `!!window.htmx`)
	js[bool](t, ctx, `document.body.dataset.marker = "kept"; true`)
	do(t, ctx, chromedp.Click(`#articles a[href="/admin/articles?page=2"]`))
	waitFor(t, ctx, `location.search === "?page=2" && document.querySelector("[data-slot=pagination-link][aria-current=page]").textContent.trim() === "2"`)
	if js[string](t, ctx, `document.body.dataset.marker || ""`) != "kept" {
		t.Error("pagination reloaded the whole page")
	}
	do(t, ctx, chromedp.NavigateBack())
	waitFor(t, ctx, `location.search === "" && document.querySelector("[data-slot=pagination-link][aria-current=page]")?.textContent.trim() === "1"`)
	noErrors(t, ctx)
}

// L2：新建对话框：422 在对话框内回显；成功后关闭对话框、按当前筛选刷新列表、弹出 toast。
func TestBrowserCreateDialog(t *testing.T) {
	ctx := tab(t)
	base := start(t)
	open(t, ctx, base+"/admin/articles?status=draft")
	before := js[string](t, ctx, total)

	do(t, ctx, chromedp.Click("#new-article"))
	waitFor(t, ctx, dialogOpen+`("article-dialog")`)
	waitFor(t, ctx, `document.querySelector("#article-dialog").contains(document.activeElement)`)
	shot(t, ctx, "dialog-open")

	do(t, ctx, chromedp.Click("#save-article"))
	waitFor(t, ctx, `!!document.querySelector("#title-error")`)
	t.Logf("422 errors: title=%q slug=%q", js[string](t, ctx, `document.querySelector("#title-error").textContent.trim()`), js[string](t, ctx, `document.querySelector("#slug-error")?.textContent.trim() ?? ""`))
	if !js[bool](t, ctx, dialogOpen+`("article-dialog")`) {
		t.Fatal("dialog closed after 422")
	}
	if js[string](t, ctx, `document.querySelector("#title-input").getAttribute("aria-invalid")`) != "true" {
		t.Error("aria-invalid not set")
	}
	shot(t, ctx, "dialog-422")

	do(t, ctx, chromedp.SendKeys("#title-input", "新的草稿"), chromedp.SendKeys("#slug-input", "Bad Slug"), chromedp.Click("#save-article"))
	waitFor(t, ctx, `!document.querySelector("#title-error") && document.querySelector("#slug-error")?.textContent.includes("小写字母")`)
	if js[string](t, ctx, `document.querySelector("#title-input").value`) != "新的草稿" {
		t.Error("title input lost after morph")
	}

	// 业务规则：slug 已被占用。
	js[bool](t, ctx, `document.querySelector("#slug-input").value = "post-1"; true`)
	do(t, ctx, chromedp.Click("#save-article"))
	waitFor(t, ctx, `document.querySelector("#slug-error")?.textContent.includes("已被使用")`)

	js[bool](t, ctx, `document.querySelector("#slug-input").value = "new-draft"; true`)
	do(t, ctx, chromedp.Click("#save-article"))
	waitFor(t, ctx, dialogGone+`("article-dialog")`)
	waitFor(t, ctx, toastWith+`("已保存")`)
	waitFor(t, ctx, total+` !== "`+before+`"`)
	if !js[bool](t, ctx, `location.search === "?status=draft" && [...document.querySelectorAll("#articles tbody tr")].every(tr => tr.textContent.includes("草稿"))`) {
		t.Error("list refresh lost the status filter")
	}
	shot(t, ctx, "saved-toast")

	// 对话框可以再次打开（同一个 id 的新声明替换旧的）。
	do(t, ctx, chromedp.Click("#new-article"))
	waitFor(t, ctx, dialogOpen+`("article-dialog") && document.querySelector("#title-input").value === ""`)
	if n := js[int](t, ctx, `document.querySelectorAll("#article-dialog").length`); n != 1 {
		t.Errorf("%d #article-dialog elements after reopening", n)
	}
	do(t, ctx, chromedp.KeyEvent("\x1b")) // Escape
	waitFor(t, ctx, `!`+dialogOpen+`("article-dialog")`)
	noErrors(t, ctx)
}

// L3：行内下拉菜单里的 hx-get（shadcn-templ #616 的场景）打开编辑框与删除确认框。
func TestBrowserRowActions(t *testing.T) {
	ctx := tab(t)
	base := start(t)
	open(t, ctx, base+"/admin/articles?sort=title")
	id := js[string](t, ctx, `document.querySelector("#articles tbody tr").id.replace("article-", "")`)

	do(t, ctx, chromedp.Click(`#article-`+id+` [data-slot=dropdown-menu-trigger]`))
	waitFor(t, ctx, `!!document.querySelector("#edit-`+id+`") && document.querySelector("#edit-`+id+`").checkVisibility()`)
	shot(t, ctx, "row-menu")
	do(t, ctx, chromedp.Click("#edit-"+id))
	waitFor(t, ctx, dialogOpen+`("article-dialog") && document.querySelector("#title-input").value !== ""`)
	js[bool](t, ctx, `document.querySelector("#title-input").value = "AAA 改过的标题"; true`)
	do(t, ctx, chromedp.Click("#save-article"))
	waitFor(t, ctx, dialogGone+`("article-dialog") && document.querySelector("#article-`+id+`")?.textContent.includes("AAA 改过的标题")`)

	// 删除：菜单 → 确认框 → hx-delete。
	do(t, ctx, chromedp.Click(`#article-`+id+` [data-slot=dropdown-menu-trigger]`))
	waitFor(t, ctx, `document.querySelector("#delete-`+id+`")?.checkVisibility()`)
	do(t, ctx, chromedp.Click("#delete-"+id))
	waitFor(t, ctx, dialogOpen+`("delete-dialog")`)
	shot(t, ctx, "confirm-delete")
	do(t, ctx, chromedp.Click("#confirm-delete"))
	waitFor(t, ctx, `!`+dialogOpen+`("delete-dialog") && !document.querySelector("#article-`+id+`") && `+toastWith+`("已删除")`)

	// 局部请求出错：404 渲染为错误 toast（ErrorFragment 替换进 #flash）。
	js[bool](t, ctx, `htmx.ajax("GET", "/admin/articles/`+id+`/edit", {target: "#modal"}); true`)
	waitFor(t, ctx, `[...document.querySelectorAll('[data-slot=toast]')].some(el => el.querySelector('[data-slot=toast-title]')?.textContent.trim().length > 0 && el.textContent.includes("Not Found") || el.textContent.includes("不存在") || el.textContent.includes("找不到"))`)
	t.Logf("error toast: %q", js[string](t, ctx, `[...document.querySelectorAll('[data-slot=toast]')].map(e => e.textContent.trim()).join(" | ")`))
	noErrors(t, ctx)
}

// P1：落地页：暗色模式、价格切换（View Transition）、普通表单的 422 与跳转后的 flash toast。
func TestBrowserLanding(t *testing.T) {
	ctx := tab(t)
	base := start(t)
	open(t, ctx, base+"/")
	shot(t, ctx, "landing-light")

	do(t, ctx, chromedp.Click("#theme-toggle"))
	waitFor(t, ctx, `document.documentElement.classList.contains("dark")`)
	open(t, ctx, base+"/")
	if !js[bool](t, ctx, `document.documentElement.classList.contains("dark")`) {
		t.Error("dark mode not persisted across reload")
	}
	shot(t, ctx, "landing-dark")

	js[bool](t, ctx, `window.__vt = 0; window.__vtActive = 0; const o = document.startViewTransition?.bind(document); if (o) document.startViewTransition = (...a) => { __vt++; __vtActive++; const v = o(...a); v.finished.finally(() => __vtActive--); return v }; true`)
	do(t, ctx, chromedp.Click(`#pricing-plans button:not([data-active])`))
	waitFor(t, ctx, `[...document.querySelectorAll(".plan-price")].some(e => e.textContent.includes("38"))`)
	t.Logf("view transitions started: %d", js[int](t, ctx, `window.__vt`))
	// 过渡进行中，点击落在过渡层上；等过渡结束。
	waitFor(t, ctx, `__vt > 0 && __vtActive === 0`)

	js[bool](t, ctx, `document.querySelector("#email").value = "not-an-email"; true`)
	do(t, ctx, chromedp.Click("#subscribe-submit"))
	waitFor(t, ctx, `!!document.querySelector("#email-error")`)
	t.Logf("email error: %q", js[string](t, ctx, `document.querySelector("#email-error").textContent.trim()`))
	js[bool](t, ctx, `document.querySelector("#email").value = "a@example.com"; true`)
	do(t, ctx, chromedp.Click("#subscribe-submit"))
	waitFor(t, ctx, `location.hash === "#subscribe" && `+toastWith+`("订阅成功")`)
	shot(t, ctx, "landing-flash")
	// flash 只显示一次。
	open(t, ctx, base+"/")
	time.Sleep(300 * time.Millisecond)
	if js[bool](t, ctx, toastWith+`("订阅成功")`) {
		t.Error("flash shown twice")
	}
	noErrors(t, ctx)
}

// 手机宽度下落地页没有横向滚动。
func TestBrowserMobile(t *testing.T) {
	ctx := tab(t)
	base := start(t)
	do(t, ctx, chromedp.EmulateViewport(390, 844))
	for _, p := range []string{"/", "/admin/articles"} {
		open(t, ctx, base+p)
		if w := js[int](t, ctx, `document.documentElement.scrollWidth - innerWidth`); w > 0 {
			t.Errorf("%s: horizontal overflow %dpx", p, w)
		}
		shot(t, ctx, "mobile"+strings.ReplaceAll(p, "/", "-"))
	}
}
