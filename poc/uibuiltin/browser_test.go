package uibuiltin_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// 浏览器场景需要本机 Chrome：POC_BROWSER=1 go test -run TestBrowser -v .
// SHOTS=dir 时把关键画面截图保存到 dir。

func tab(t *testing.T) context.Context {
	t.Helper()
	if os.Getenv("POC_BROWSER") == "" {
		t.Skip("set POC_BROWSER=1 to run browser scenarios")
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath("/usr/bin/google-chrome"), chromedp.Flag("lang", "zh-CN"), chromedp.Flag("accept-lang", "zh-CN"))...)
	ctx, cancel := chromedp.NewContext(alloc)
	t.Cleanup(func() { cancel(); cancelAlloc() })
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
	loc, _ := chromedp.Run(ctx, chromedp.Evaluate[string]("location.href"))
	t.Fatalf("timed out waiting for %s (location %s)", expr, loc)
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
const dialogGone = `(id => !window.templ.dialog.isOpen(id) && ![...document.querySelectorAll("[data-slot$=dialog-overlay]")].some(e => e.checkVisibility()))`
const toastWith = `(s => [...document.querySelectorAll('[data-slot=toast]')].some(el => el.textContent.includes(s)))`
const total = `document.querySelector("#total").textContent.trim()`

// style 返回元素的计算样式属性。
func style(t *testing.T, ctx context.Context, sel, prop string) string {
	t.Helper()
	return js[string](t, ctx, `getComputedStyle(document.querySelector(`+quote(sel)+`)).getPropertyValue(`+quote(prop)+`).trim()`)
}

func quote(s string) string { return "`" + s + "`" }

// B1–B5：运营后台（nova、预编译样式表、shadcn toast）。
func TestBrowserOps(t *testing.T) {
	ctx := tab(t)
	base := start(t)

	// 登录页：验证失败在字段旁显示错误，组件按 aria-invalid 切换样式。
	open(t, ctx, base+"/ops/articles")
	waitFor(t, ctx, `location.pathname === "/ops/login"`)
	if c := js[string](t, ctx, `document.documentElement.className`); c != "style-nova" && c != "style-nova dark" {
		t.Errorf("html class = %q", c)
	}
	do(t, ctx, chromedp.Click(`#login button[type=submit]`))
	waitFor(t, ctx, `document.querySelector("#email")?.getAttribute("aria-invalid") === "true"`)
	shot(t, ctx, "ops-login-errors")
	do(t, ctx, chromedp.SendKeys("#email", "ops@example.com"), chromedp.SendKeys("#password", "secret"), chromedp.Click(`#login button[type=submit]`))
	waitFor(t, ctx, `location.pathname === "/ops/articles" && !!window.templ`)
	js[bool](t, ctx, trapErrors)
	if got := js[string](t, ctx, total); got != "共 57 篇" {
		t.Fatalf("total = %q", got)
	}
	shot(t, ctx, "ops-list")

	// 列表：搜索和 webui.Pagination 只替换列表区域，地址栏同步。
	js[bool](t, ctx, `document.body.dataset.marker = "kept"; true`)
	do(t, ctx, chromedp.SendKeys("#q", "htmx"))
	waitFor(t, ctx, `location.search === "?q=htmx" && `+total+` === "共 5 篇"`)
	do(t, ctx, chromedp.Navigate(base+"/ops/articles"))
	waitFor(t, ctx, `!!window.templ && !!window.htmx`)
	js[bool](t, ctx, trapErrors+`; document.body.dataset.marker = "kept"; true`)
	do(t, ctx, chromedp.Click(`[data-slot=pagination-link][href="/ops/articles?page=2"]`))
	waitFor(t, ctx, `location.search === "?page=2" && document.querySelector('[aria-current=page]')?.textContent.trim() === "2"`)
	if js[string](t, ctx, `document.body.dataset.marker`) != "kept" {
		t.Error("pagination reloaded the whole page")
	}

	// 对话框：422 在对话框内显示错误；成功后服务端关闭对话框、刷新列表、弹出 toast。
	do(t, ctx, chromedp.Click("#new-article"))
	waitFor(t, ctx, dialogOpen+`("article-dialog")`)
	do(t, ctx, chromedp.Click(`#article-form button[type=submit]`))
	waitFor(t, ctx, `document.querySelector("#title")?.getAttribute("aria-invalid") === "true" && `+dialogOpen+`("article-dialog")`)
	shot(t, ctx, "ops-dialog-422")
	do(t, ctx, chromedp.SendKeys("#title", "新文章"), chromedp.SendKeys("#slug", "brand-new"), chromedp.Click(`#article-form button[type=submit]`))
	waitFor(t, ctx, dialogGone+`("article-dialog") && `+toastWith+`("文章已创建") && `+total+` === "共 58 篇"`)
	shot(t, ctx, "ops-created-toast")

	// 删除确认框；局部请求的错误由 ErrorFragment 显示为 toast。
	do(t, ctx, chromedp.Click(`[data-delete]`))
	waitFor(t, ctx, dialogOpen+`("delete-dialog")`)
	do(t, ctx, chromedp.Click("#confirm-delete"))
	waitFor(t, ctx, dialogGone+`("delete-dialog") && `+toastWith+`("已删除") && `+total+` === "共 57 篇"`)
	js[bool](t, ctx, `htmx.ajax("DELETE", "/ops/articles/99999", {target: "#articles"}); true`)
	waitFor(t, ctx, `[...document.querySelectorAll('[data-slot=toast]')].some(el => el.getAttribute("data-type") === "error" || el.textContent.includes("Not Found") || el.textContent.includes("不存在"))`)

	// 暗色模式：切换后刷新仍然保留。
	do(t, ctx, chromedp.Click("#theme-toggle"))
	waitFor(t, ctx, `document.documentElement.classList.contains("dark")`)
	do(t, ctx, chromedp.Reload())
	waitFor(t, ctx, `document.readyState === "complete" && document.documentElement.classList.contains("dark")`)
	shot(t, ctx, "ops-dark")
	js[bool](t, ctx, `localStorage.removeItem("theme"); true`)
	noErrors(t, ctx)
}

// B6–B8：租户端（maia、自己的 Tailwind 样式表、品牌主色、自己的提示和错误横幅）。
func TestBrowserTenant(t *testing.T) {
	ctx := tab(t)
	base := start(t)

	open(t, ctx, base+"/ops/login")
	opsRadius := style(t, ctx, "#email", "border-radius")
	opsPrimary := js[string](t, ctx, `getComputedStyle(document.documentElement).getPropertyValue("--primary").trim()`)

	open(t, ctx, base+"/app/")
	waitFor(t, ctx, `location.pathname === "/app/login"`)
	js[bool](t, ctx, trapErrors)
	if c := js[string](t, ctx, `document.documentElement.className`); c != "style-maia" {
		t.Errorf("html class = %q", c)
	}
	primary := js[string](t, ctx, `getComputedStyle(document.documentElement).getPropertyValue("--primary").trim()`)
	if primary != "oklch(0.55 0.22 275)" || primary == opsPrimary {
		t.Errorf("tenant --primary = %q (ops %q)", primary, opsPrimary)
	}
	// maia 风格加上 radius: 1rem，输入框的圆角与运营后台不同。
	if r := style(t, ctx, "#email", "border-radius"); r == opsRadius {
		t.Errorf("input radius same in both areas: %s", r)
	} else {
		t.Logf("input border-radius: ops %s, tenant %s", opsRadius, r)
	}
	// Class 中的工具类覆盖风格：方角、品牌色背景。
	if r := style(t, ctx, "#login-submit", "border-top-left-radius"); r != "0px" {
		t.Errorf("rounded-none did not override the style: %s", r)
	}
	if bg := style(t, ctx, "#login-submit", "background-color"); bg != "oklch(0.55 0.22 275)" {
		t.Errorf("bg-brand-600 did not override the style: %s", bg)
	}
	// 自己的设计令牌：品牌色的展示区和展示字体。
	if bg := style(t, ctx, "main section", "background-color"); bg != "oklch(0.55 0.22 275)" {
		t.Errorf("hero background = %s", bg)
	}
	shot(t, ctx, "tenant-login")

	do(t, ctx, chromedp.SendKeys("#email", "alice@acme.test"), chromedp.SendKeys("#password", "secret"), chromedp.Click("#login-submit"))
	waitFor(t, ctx, `location.pathname === "/app/" && [...document.querySelectorAll(".app-toast")].some(e => e.textContent.includes("欢迎回来"))`)
	js[bool](t, ctx, trapErrors)
	if n := js[int](t, ctx, `document.querySelectorAll("[data-slot=toast-viewport]").length`); n != 0 {
		t.Errorf("tenant area rendered the shadcn toaster")
	}
	do(t, ctx, chromedp.Click(`[data-star]`))
	waitFor(t, ctx, `[...document.querySelectorAll(".app-toast")].some(e => e.textContent.includes("已收藏"))`)
	shot(t, ctx, "tenant-home")

	// 局部请求出错：租户端自己的错误横幅。
	js[bool](t, ctx, `htmx.ajax("POST", "/app/articles/99999/star", {source: document.querySelector("[data-star]"), target: document.querySelector("[data-star]"), swap: "none"}); true`)
	waitFor(t, ctx, `!!document.querySelector("#app-errors .app-error")`)
	noErrors(t, ctx)
}
