// TestTSRoundTrip 用 bun 运行它：生成的 TypeScript 客户端调用真实的示例服务。
// BASE_URL 是示例服务的 API 地址，CLIENT 是生成的 api.ts 的绝对路径。

const { createClient, isApiError, ArticleErrors, IdentityErrors } = (await import(process.env.CLIENT!)) as typeof import("../internal/example/ts/api")
const baseURL = process.env.BASE_URL!

function check(ok: unknown, what: string, detail?: unknown): void {
  if (!ok) {
    console.error("FAIL: " + what, detail === undefined ? "" : JSON.stringify(detail))
    process.exit(1)
  }
}

async function rejects(p: Promise<unknown>, what: string): Promise<import("../internal/example/ts/api").ApiError> {
  try {
    await p
  } catch (err) {
    check(isApiError(err), what + ": not an ApiError", String(err))
    return err as import("../internal/example/ts/api").ApiError
  }
  check(false, what + ": did not reject")
  throw new Error("unreachable")
}

// 公开的接口不带 token。
const anonymous = createClient({ baseURL, language: () => "en" })
check((await anonymous.ping()).ok === true, "ping")
let err = await rejects(anonymous.article.create({ title: "x", slug: "x" }), "anonymous create")
check(err.status === 401 && isApiError(err, IdentityErrors.Unauthenticated), "401", err)

// 受保护的接口收到 401 时刷新：并发的请求只刷新一次，然后重试。
let refreshes = 0
const api = createClient({
  baseURL,
  language: () => "en",
  auth: { refresh: async () => (refreshes++, "secret") },
})
const [hello, hi] = await Promise.all([
  api.article.create({ title: "Hello", slug: "hello", tags: ["go"] }),
  api.article.create({ title: "Hi", slug: "hi" }),
])
check(refreshes === 1, "refresh once", refreshes)
check(api.getAccessToken() === "secret", "token kept")
check(hello.body.slug === "hello" && hello.headers.location === "/articles/" + hello.body.id, "create with headers", hello)
check(hi.body.tags !== undefined, "second create", hi)

// 业务错误和字段错误。
err = await rejects(api.article.create({ title: "Again", slug: "hello" }), "duplicate slug")
check(err.status === 409 && isApiError(err, ArticleErrors.SlugTaken) && err.detail !== undefined, "slug taken", err)
err = await rejects(api.article.create({ title: "", slug: "Bad Slug" }), "invalid")
check(err.status === 422 && err.fields.some((f) => f.location === "body.title"), "field errors", err.fields)
err = await rejects(api.article.get("00000000-0000-0000-0000-000000000000"), "missing")
check(err.status === 404 && err.code === ArticleErrors.NotFound, "not found", err)

// 查询参数、请求头、没有响应体的接口。
const list = await api.article.list({ q: "Hel", per_page: 10 })
check(list.total === 1 && list.items[0].slug === "hello" && list.per_page === 10, "list", list)
const published = await api.article.publish(hello.body.id, { idempotencyKey: "k1" })
check(published.status === "published", "publish", published)
check((await api.article.publish(hello.body.id, { idempotencyKey: "k1" })).id === hello.body.id, "idempotent publish")
check((await api.article.delete(hi.body.id)) === undefined, "delete returns void")

// multipart 上传。
const cover = await api.article.cover(hello.body.id, {
  alt: "封面",
  file: new File(["png!"], "cover.png", { type: "image/png" }),
  extras: [new File(["a"], "a.txt"), new File(["b"], "b.txt")],
})
check(cover.filename === "cover.png" && cover.size === 4 && cover.alt === "封面" && cover.extras.join() === "a.txt,b.txt", "upload", cover)

// 事件流：未知的事件被跳过；lastEventId 从上次的位置继续。
const events: string[] = []
for await (const ev of api.article.events()) {
  events.push(ev.event)
  if (ev.event === "changed") check(ev.data.title === "Hello", "changed data", ev)
}
check(events.join() === "changed,deleted,message", "events", events)
const resumed: string[] = []
for await (const ev of api.article.events({}, { lastEventId: "3" })) resumed.push(ev.event)
check(resumed.join() === "message", "resumed events", resumed)
err = await rejects(
  (async () => {
    for await (const _ of api.article.events({ tag: "missing" })) {
    }
  })(),
  "stream error",
)
check(err.code === ArticleErrors.NotFound, "stream error before events", err)

// 刷新失败：调用 onSignedOut，原来的 401 抛给调用方。
let signedOut = false
const expired = createClient({ baseURL, auth: { refresh: async () => null, onSignedOut: () => (signedOut = true) } })
err = await rejects(expired.article.create({ title: "x", slug: "x" }), "refresh failed")
check(err.status === 401 && signedOut && expired.getAccessToken() === "", "signed out", err)

// 403 auth.confirm_required：确认身份后重试一次。
let calls = 0
let confirms = 0
const confirming = createClient({
  baseURL,
  fetch: async (input, init) => {
    calls++
    if (calls === 1) {
      return new Response(JSON.stringify({ status: 403, code: "auth.confirm_required", detail: "confirm" }), { status: 403 })
    }
    return fetch(input, init)
  },
  onConfirmRequired: async () => (confirms++, true),
})
check((await confirming.ping()).ok === true && confirms === 1 && calls === 2, "confirm and retry", { calls, confirms })

console.log("roundtrip ok")
