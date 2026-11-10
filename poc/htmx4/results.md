# 结果与结论

环境：Go 1.27.3、templ v0.3.1100、htmx 4.0.1、Alpine 3.17.5、Chrome（chromedp v0.23.0 headless）、nginx:alpine。

## 场景

| # | 场景 | 结果 |
|---|---|---|
| F | `web.Form`：urlencoded → 按 schema 转换类型 → `huma.Validate` → 中文提示 | ✅ minLength/maxLength（按字符计）、minimum/maximum、pattern + patternDescription、email、整数类型、必填、maxItems 都能反解成中文；未知字段忽略；未勾选的复选框为 false |
| B1 | 未登录访问页面 | ✅ 页面请求 303 `/login?next=…`；htmx 请求 204 + `HX-Redirect`，next 取自 `HX-Current-URL` |
| B2 | 表单 422 回显（`hx-swap="outerMorph"`） | ✅ htmx 4 默认替换 4xx；Alpine 状态（展开的高级选项）经 hx-alpine-compat 保留，morph 后 Alpine 继续响应输入；成功后 `HX-Redirect` 回列表 |
| B3 | 普通 `hx-post` 收到 `text/event-stream` | ✅ 每 700ms 一步，逐步追加 |
| B4 | `defaultTimeout=1000ms` 下 2.1s 的流式响应 | ✅ 3/3 步全部到达，未被中止 |
| B5 | 两人聊天室 | ✅ 输入状态 `<hx-partial>` 推送；空 `<hx-partial>` 能清空目标；消息送达；发送后表单重置（需 `.self`，见问题 A） |
| B6 | 长连接 + `defaultTimeout=1000ms`，空闲 2.5s | ✅ 仍然收到消息，没有重连 |
| B7 | 服务端断开所有流，断线期间发消息 | ✅ 自动重连，`Last-Event-ID` 补发，恰好一次 |
| B8 | 局部请求 403 | ✅ `HX-Retarget: #flash` 放入错误片段，`#main` 不变；整页请求渲染错误页 |
| B9 | htmx 请求收到普通 303 | ⚠️ fetch 自动跟随，跳转后的请求仍带 `HX-Request`，整页布局被塞进 `#main`（出现两个导航栏），见问题 C |
| S1 | 流式路由挂在 `httpx.Timeout`（1s）之后 | ❌ 1s 时被切断；不挂 Timeout 的路由组 ✅ |
| S2 | `WriteTimeout=1s` | 普通 Flush 循环只送达 4/10 条；`web.NewStream` 用 `ResponseController.SetWriteDeadline(time.Time{})` 取消写超时 ✅（穿过 chi 和 scs 的包装器） |
| S3 | 有打开的流时停机（ShutdownTimeout 3s） | 无排空信号：等满 3s；`OnDraining: drain.Start` 通知流结束：0s ✅ |
| S4 | 流的访问日志 | ✅ 流结束后写出一条，带完整时长 |
| S5 | 页面与 API 共存 | ✅ API 挂在 `/api` 子路由，`/api/nope` 返回 JSON 404，`/nope` 返回 HTML 404；`CrossOriginProtection` 放行 htmx 请求、拒绝跨站 POST |

<a id="nginx"></a>
**nginx**（手动）：默认 `proxy_buffering on`，`web.NewStream` 发出 `X-Accel-Buffering: no` 后，三步仍按 700ms 间隔到达。另外，`proxy_set_header Host $host` 会丢掉端口，`CrossOriginProtection` 因 Origin 与 Host 不一致拒绝**所有** POST，需改用 `$http_host`。这一点对 REST API 同样适用，应写进部署文档。

## 问题

**A. htmx 事件冒泡。** 聊天表单用 `x-on:htmx:after:request="$el.reset()"`，输入框的「正在输入」请求完成时事件冒泡到表单，把正在输入的内容清空。改为 `x-on:htmx:after:request.self` 解决。文档需要提醒。

**B. 重新渲染丢失非字符串字段的原始输入。** 优先级填 `abc` 时，`Form` 解码后为 0，回显的是 `0`。框架需要提供原始输入（类似 Laravel 的 `old()`），例如 `Form` 同时返回 `url.Values`，或返回一个能取原始值的表单对象。

**C. 普通 303 与 htmx 请求。** 处理器对 htmx 请求返回普通 303 时，结果是嵌套的整页布局。约定只用 `web.Redirect`；框架还可以在 `Render` 或一个中间件里把 htmx 请求的 3xx 自动改写为 `HX-Redirect`，避免误用。

**D. 验证消息反解依赖 Huma 的英文格式串。** 规则表由 `validation.Msg*` 生成，Huma 改动格式时测试会失败，不会静默出错；无法识别的消息原样返回英文。接入框架时改用 i18n 消息，并在升级 Huma 时跑这组测试。

**E. `httpx.Defaults` 包含 Timeout。** 流式路由不能用 `Defaults`。PoC 手工组装了去掉 Timeout 的中间件栈，再按路由组安装 Timeout。框架需要二选一：Timeout 移出 `Defaults`，或者提供不含 Timeout 的组合。

**F. 流中修改会话不会保存。** scs 在第一次写出响应时提交会话，流开始之后的会话修改都会丢失。文档说明即可。

## 未覆盖

- `hx-boost` 和 htmx 4 的后退（重新请求整页、`HX-Request-Type: full`）。
- multipart 上传、morph 时光标位置的保留。
- 浏览器 HTTP/1.1 每个源最多 6 条连接：每个标签页占一条 SSE，打开多个标签页会耗尽连接。生产环境需要 HTTP/2（通常在反向代理终止 TLS）。
- 多实例广播（PoC 用单实例内存 Hub）。

## 对框架的结论

templ + htmx 4 + Alpine 可行，`httpx/web` 的原型不到 500 行（不含测试），没有引入 templ 以外的依赖。P1 的范围：

1. **`httpx/web`**：
   - `Render`、`IsHTMX` / `IsPartial`、`Redirect`（加上问题 C 的防护）；
   - 错误映射：局部请求 `HX-Retarget` 到片段，整页请求渲染错误页；
   - `Form`（加上原始输入，接入 i18n）；
   - `Stream`（取消写超时、`X-Accel-Buffering`、心跳）和 `Drain`；
   - 内嵌 htmx / hx-sse / hx-alpine-compat / Alpine，以及 `Scripts` 组件。
2. **`session.RequirePage`**。
3. **`httpx`**：
   - Timeout 的位置（问题 E）；
   - `rest.HTTPStatus` 下沉为 `httpx.StatusOf`；
   - `Serve` 的排空信号直接接到流上（PoC 用现有的 `OnDraining` 已经够用）。
4. **文档**：API 挂在 `/api`；部署时反向代理用 `$http_host`、开启 HTTP/2；Alpine 监听 htmx 事件时加 `.self`。
