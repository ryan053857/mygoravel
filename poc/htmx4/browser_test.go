package htmx4_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// 浏览器场景需要本机 Chrome：POC_BROWSER=1 go test -run TestBrowser .
func browserTab(t *testing.T) context.Context {
	t.Helper()
	if os.Getenv("POC_BROWSER") == "" {
		t.Skip("set POC_BROWSER=1 to run browser scenarios")
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath("/usr/bin/google-chrome"))...)
	root, cancel := chromedp.NewContext(alloc)
	t.Cleanup(func() { cancel(); cancelAlloc() })
	if err := chromedp.Do(root, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}
	rootOf[root] = root
	tab := otherUser(t, root)
	rootOf[tab] = root
	return tab
}

// rootOf 记录每个标签页所属的根 context，用来在同一个浏览器里开新的 browser context。
var rootOf = map[context.Context]context.Context{}

// otherUser 在同一个浏览器里开一个独立的 browser context（独立 cookie）。
func otherUser(t *testing.T, ctx context.Context) context.Context {
	t.Helper()
	c, cancel := chromedp.NewContext(rootOf[ctx], chromedp.WithNewBrowserContext())
	t.Cleanup(cancel)
	if err := chromedp.Do(c, chromedp.Navigate("about:blank")); err != nil {
		t.Fatal(err)
	}
	return c
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

// waitFor 轮询 JS 表达式直到为真；页面跳转期间的求值错误忽略。
func waitFor(t *testing.T, ctx context.Context, expr string, d time.Duration) {
	t.Helper()
	if !poll(ctx, expr, d) {
		t.Fatalf("timed out after %v waiting for %s (location %s)", d, expr, js[string](t, ctx, "location.href"))
	}
}

func poll(ctx context.Context, expr string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok, err := chromedp.Run(ctx, chromedp.Evaluate[bool](expr)); err == nil && ok {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

func login(t *testing.T, ctx context.Context, s *server, email, path string) {
	t.Helper()
	do(t, ctx,
		chromedp.Navigate(s.URL+path),
		chromedp.WaitVisible("#login-form"),
		chromedp.SendKeys("#email", email),
		chromedp.SendKeys("#password", "secret"),
		chromedp.Click("#login"),
	)
	waitFor(t, ctx, `location.pathname === "`+strings.Split(path, "?")[0]+`"`, 5*time.Second)
}

func text(t *testing.T, ctx context.Context, sel string) string {
	return js[string](t, ctx, `(document.querySelector("`+sel+`")?.textContent ?? "").trim()`)
}

// B1–B2：未登录跳转、表单 422 回显、Alpine 状态在 outerMorph 后保留、成功后 HX-Redirect。
func TestBrowserForm(t *testing.T) {
	ctx := browserTab(t)
	s := start(t, serverOptions{})
	login(t, ctx, s, "alice@example.com", "/admin/articles/1/edit")

	do(t, ctx, chromedp.Click("#toggle-advanced"), chromedp.WaitVisible("#advanced"))
	js[bool](t, ctx, `document.querySelector("#title").value = ""; document.querySelector("#slug").value = "Bad Slug"; document.querySelector("#priority").value = "abc"; true`)
	do(t, ctx, chromedp.Click("#save"))
	waitFor(t, ctx, `!!document.querySelector("#error-title")`, 5*time.Second)
	t.Logf("errors: title=%q slug=%q priority=%q", text(t, ctx, "#error-title"), text(t, ctx, "#error-slug"), text(t, ctx, "#error-priority"))
	if v := js[bool](t, ctx, `getComputedStyle(document.querySelector("#advanced")).display !== "none"`); !v {
		t.Error("Alpine state (advanced=true) lost after 422 outerMorph")
	}
	t.Logf("priority input after 422: %q", js[string](t, ctx, `document.querySelector("#priority").value`))
	t.Logf("title counter after 422: %q", text(t, ctx, "#title-count"))

	do(t, ctx, chromedp.SendKeys("#title", "新标题"))
	if c := text(t, ctx, "#title-count"); c != "3" {
		t.Errorf("Alpine counter after morph = %q, want 3", c)
	}
	js[bool](t, ctx, `document.querySelector("#slug").value = "new-title"; document.querySelector("#priority").value = "5"; true`)
	do(t, ctx, chromedp.Click("#save"))
	waitFor(t, ctx, `location.pathname === "/admin/articles" && document.body.textContent.includes("新标题")`, 5*time.Second)
}

// B3–B4：普通 hx-post 收到流式响应，逐步替换；默认请求超时对流式响应的影响。
func TestBrowserStreamingResponse(t *testing.T) {
	ctx := browserTab(t)
	s := start(t, serverOptions{})
	login(t, ctx, s, "alice@example.com", "/tasks")

	do(t, ctx, chromedp.Click("#run"))
	var seen []int
	began := time.Now()
	for time.Since(began) < 3*time.Second {
		n := js[int](t, ctx, `document.querySelectorAll("#progress .step").length`)
		if len(seen) == 0 || seen[len(seen)-1] != n {
			seen = append(seen, n)
			t.Logf("%v: %d steps", time.Since(began).Round(100*time.Millisecond), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(seen) < 3 || seen[len(seen)-1] != 3 {
		t.Fatalf("steps over time: %v", seen)
	}

	// defaultTimeout = 1000ms，流式响应需要 2.1 秒。
	do(t, ctx, chromedp.Navigate(s.URL+"/tasks?timeout=1000"), chromedp.WaitVisible("#run"), chromedp.Click("#run"))
	time.Sleep(3 * time.Second)
	t.Logf("with defaultTimeout=1000ms a 2.1s streaming response delivered %d/3 steps", js[int](t, ctx, `document.querySelectorAll("#progress .step").length`))
}

// B5–B7：两个用户的聊天室：输入状态、消息、表单重置、长连接不受 defaultTimeout 影响、断线重连补发。
func TestBrowserChat(t *testing.T) {
	alice := browserTab(t)
	bob := otherUser(t, alice)
	s := start(t, serverOptions{})
	login(t, alice, s, "alice@example.com", "/rooms/lobby")
	login(t, bob, s, "bob@example.com", "/rooms/lobby?timeout=1000")
	waitFor(t, bob, `true`, time.Second)
	time.Sleep(500 * time.Millisecond)
	conns := s.App.Hub.Connections.Load()
	t.Logf("connections after both joined: %d", conns)

	do(t, alice, chromedp.SendKeys("#chat-input", "hi bo"))
	waitFor(t, bob, `document.querySelector("#typing-text")?.textContent.includes("alice@example.com")`, 3*time.Second)
	t.Log("bob sees alice typing")
	cleared := poll(bob, `!document.querySelector("#typing-text")`, 5*time.Second)
	t.Logf("typing indicator cleared by empty <hx-partial>: %v (typing html=%q)", cleared, js[string](t, bob, `document.querySelector("#typing").innerHTML`))

	do(t, alice, chromedp.SendKeys("#chat-input", "b"), chromedp.Click("#send"))
	waitFor(t, bob, `document.querySelector("#messages").textContent.includes("hi bob")`, 3*time.Second)
	waitFor(t, alice, `document.querySelector("#chat-input").value === ""`, 2*time.Second)
	t.Log("message delivered; alice's form reset by x-on:htmx:after:request")

	// bob 的 defaultTimeout 是 1 秒，空闲 2.5 秒后长连接仍应收到消息且没有重连。
	time.Sleep(2500 * time.Millisecond)
	s.App.Hub.Post("lobby", "carol@example.com", "still connected?")
	waitFor(t, bob, `document.querySelector("#messages").textContent.includes("still connected?")`, 3*time.Second)
	t.Logf("connections after idle: %d (before %d)", s.App.Hub.Connections.Load(), conns)

	// 断开所有流，断线期间发一条消息，重连后应通过 Last-Event-ID 补发且不重复。
	s.App.Hub.KickAll()
	s.App.Hub.Post("lobby", "carol@example.com", "while you were away")
	waitFor(t, bob, `document.querySelector("#messages").textContent.includes("while you were away")`, 5*time.Second)
	time.Sleep(500 * time.Millisecond)
	n := js[int](t, bob, `[...document.querySelectorAll("#messages .message")].filter(e => e.textContent.includes("while you were away")).length`)
	t.Logf("after reconnect: connections=%d, replayed copies=%d", s.App.Hub.Connections.Load(), n)
	if n != 1 {
		t.Errorf("replayed message appears %d times", n)
	}
}

// B8–B9：局部请求的 403 片段（HX-Retarget），以及 htmx 请求收到普通 303 的行为。
func TestBrowserErrorsAndRedirects(t *testing.T) {
	ctx := browserTab(t)
	s := start(t, serverOptions{})
	login(t, ctx, s, "alice@example.com", "/")

	do(t, ctx, chromedp.Click("#admin-only"))
	waitFor(t, ctx, `!!document.querySelector("#flash #error-fragment")`, 3*time.Second)
	t.Logf("403 fragment in #flash: %q; #main still home: %v", text(t, ctx, "#flash"), js[bool](t, ctx, `!!document.querySelector("#home")`))

	do(t, ctx, chromedp.Click("#redirect-303"))
	time.Sleep(time.Second)
	t.Logf("after plain 303: location=%s, nested <nav> count=%d, #main has articles=%v",
		js[string](t, ctx, `location.pathname`), js[int](t, ctx, `document.querySelectorAll("nav").length`),
		js[bool](t, ctx, `!!document.querySelector("#main #articles")`))
}
