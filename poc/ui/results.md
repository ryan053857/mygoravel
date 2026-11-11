# 结果与结论

环境：Go 1.27.3、templ v0.3.1100、htmx 4.0.1（框架内嵌）、shadcn-templ CLI v2.0.0（preset nova）、Tailwind CSS v4.3.3 standalone、Chrome（chromedp v0.23.0 headless）。

## 场景

| # | 场景 | 结果 |
|---|---|---|
| L1 | 搜索、状态筛选、排序、翻页只替换列表区域，地址栏同步，后退恢复 | ✅ 见问题 A、B |
| L2 | 对话框中新建：422 在对话框内回显（`outerMorph`），`aria-invalid` 和 field 错误样式；成功后关闭对话框，按当前筛选刷新列表，弹出 toast；对话框可以再次打开，Escape 关闭 | ✅ 中文验证消息；slug 被占用用 `SetError` 显示在字段旁；重开后只有一个 `#article-dialog`，见问题 C |
| L3 | 行内下拉菜单里的 `hx-get` 打开编辑框和删除确认框（上游 #616 的场景）；列表刷新后菜单再次打开；`hx-delete` 后关闭确认框 | ✅ |
| L3' | 局部请求出错（404）：`ErrorFragment` 渲染为 `toast.Toast`，替换进 `#flash` 后由 toast.js 弹出 | ✅ 提示是英文“Not Found”，因为应用没有翻译 `article.not_found`，属于应用的问题 |
| P1 | 落地页：品牌令牌、暗色模式（首帧前设置，刷新后保留）、价格切换（`hx-swap="outerHTML transition:true"` 触发 View Transition） | ✅ |
| P2 | 普通表单（不用 htmx）：422 整页回显；成功后 303 跳转，flash 显示一次 | ✅ |
| M | 手机宽度（390px）下落地页和后台没有横向滚动 | ✅ 后台需要给 `main` 加上 `min-w-0`（flex 子项的常见问题，与组件无关） |
| S1 | 生产模式的静态资源：CSS 按内容加指纹（`app.6c2dee61.css`），带指纹的地址 `immutable`；文件名本身带哈希的组件脚本也是 `immutable`；原始文件名 `no-cache` + ETag，可以返回 304 | ✅ |
| S2 | 开发模式：直接读磁盘、不加指纹、`no-store` | ✅ |
| S3 | 匿名访问公开页面没有 `Set-Cookie`；flash 写入会话后才有 cookie，只显示一次 | ✅ 响应仍然带 `Vary: Cookie`（scs 添加），见问题 F |
| D | `go run ./cmd/dev`：同时运行三个 watch 进程，templ 代理注入自动刷新脚本 | ✅ 修改 `.templ` 中的文字后约 0.1s 生效（templ 的 watch 模式不需要重启）；新的 Tailwind class 立即写进 app.css；Ctrl-C 后三个进程一起退出 |

截图：
- 后台：列表、下拉菜单、422 对话框、保存后的 toast；
- 落地页：亮色和暗色。

shadcn-templ 组件开箱即用，视觉与 shadcn/ui 一致。落地页只用了 `button` 和 `input`，其余都是工具类加品牌令牌，两种页面风格可以共存。

## 体积

| | 原始 | gzip |
|---|---|---|
| `app.css`（含 tw-animate 和 shadcn 基础样式） | 95 KB | 16 KB |
| 组件 JS（22 个组件，含 floating-ui） | 136 KB | 45 KB |

复制进应用的代码：
- 约 4.8k 行 templ、9.8k 行 JS；
- `icon` 包含完整的 Lucide 图标集，源码 420 KB，编译进二进制。

## 问题

**A. 表单 GET 把空字段写进地址。** 筛选表单 `hx-push-url="true"` 推送的是 `?q=htmx&status=&sort=`。列表处理器在局部响应中设置 `HX-Push-Url`，改为规范地址；htmx 4 优先使用这个响应头。框架的列表辅助可以统一做这件事。

**B. 列表状态分散在两处，需要同步。** 筛选表单在列表区域之外，排序链接在列表区域之内，点击排序后表单里的 `sort` 已经过时，再搜索就会丢掉排序。PoC 在局部响应后附一个 `<hx-partial hx-target="#sort-input">` 同步隐藏字段。

另一个坑：表单的 `hx-trigger="input delay:300ms, change"` 会在输入框失焦时再提交一次，和随后点击的排序链接竞争。要写成 `input delay:300ms from:#q, change from:#status`。

两点都应该进框架的列表辅助和文档：“列表状态 = URL 参数”，由服务端生成链接、同步表单字段。

**C. 对话框的打开和关闭由服务端驱动，需要约定。** 组件的状态在浏览器里，PoC 用两种方式衔接：
- 打开：按钮 `hx-get` 把 `Dialog{DefaultOpen: true}` 加载进 `#modal`，加载后立即打开。编辑、删除确认都用这一种写法，数据从服务端取，id 固定。
- 关闭：成功响应带 `HX-Reswap: none`（不替换表单），再加 `HX-Trigger: {"dialog:close": {"id": "article-dialog"}, "articles:changed": true}`。布局里用一行监听器调用 `window.templ.dialog.close(id)`。列表区域监听 `articles:changed from:body`，按自己的当前地址重新加载。

这套写法通用，可以收成几个小函数（`web.Trigger`、关闭对话框的事件名）加一篇文档。

注意：对话框由服务端关闭后，焦点落到 `body`，而不是回到打开它的按钮。因为对话框是 `hx-get` 加载的，没有关联的触发元素。对无障碍有轻微影响，可以在监听器里把焦点还给 `htmx` 请求的来源元素。

**D. templ 拦截字符串形式的 `onclick`。** `templ.Attributes{"onclick": "..."}` 会被替换为注释，主题切换按钮因此不起作用。改用 `data-*` 属性加事件委托，这样也更容易配合 CSP。文档需要提醒。

**E. 测试时机。** 对话框的 `isOpen` 变为 false 后，遮罩还有约 100ms 的淡出动画。View Transition 进行中，点击会落在过渡层上。人手操作几乎碰不到，自动化测试需要等动画结束。

**F. 公开页面的 HTTP 缓存。** 匿名访问不会写 cookie，这一点没问题。但 scs 总是添加 `Vary: Cookie`，CDN 通常不会缓存带这个头的响应。营销页需要 CDN 缓存时，可以提供一种不经过会话的公开路由。代价是这种路由上不能用 flash；本 PoC 的订阅表单依赖 flash。

**G. CSP。** 组件的 `Scripts()` 支持 nonce（`templ.GetNonce`），框架的 `web.Scripts()` 还不支持；布局里的两段内联脚本也需要 nonce。本 PoC 没有启用 CSP。

**H. 全局名称。** 组件 JS 的全局对象叫 `window.templ`（`templ.dialog`、`templ.toast`），和 templ 本身同名但没有冲突；htmx 和 Alpine 都加载时没有出现 JS 错误。本 PoC 没有用到 Alpine：下拉、对话框、toast 都由组件 JS 负责。

## 未覆盖

- sidebar、combobox、calendar、chart 等复杂组件（只用了 22 个中的一部分）。
- 组件升级：`shadcn-templ add --overwrite` 与本地修改的合并。
- 后台登录（PoC 的后台是公开路由）。
- CSP、`hx-boost`、SSE 推送更新列表。

## 对框架的结论

shadcn-templ 可以直接用，框架不依赖它：组件代码属于应用。框架只需要补足下面几块，每块都不大：

1. **应用静态资源**（`static/` 原型约 110 行）：
   - 按内容哈希加指纹，长期缓存；
   - 已带哈希的文件名直接视为不可变；
   - 开发模式读磁盘；
   - 模板中取地址的方式需要定（本 PoC 用 context）。
2. **页面交互辅助**（`app/flash.go` 原型约 100 行）：
   - flash：处理器只调用 Put，下一次整页或局部响应负责显示；局部响应以 `hx-partial` 追加；
   - `HX-Trigger`；
   - 局部响应附带多个片段。
3. **列表辅助**：
   - 由 URL 参数解析列表状态、生成链接；
   - 局部响应设置 `HX-Push-Url`；
   - 同步列表区域之外的表单字段（问题 A、B）。
4. **开发命令**：`cmd/dev` 的形态可以直接用（`command.Concurrently` 运行三个 watch 进程）。是否做成框架命令待定；也可以只写进文档，由应用自己写这十几行。
5. **`web.Scripts()` 支持 nonce**（问题 G）。
6. **文档**：
   - 样式：Tailwind standalone、设计令牌、暗色模式；
   - 组件：shadcn-templ 的安装和对话框约定（问题 C）；
   - 坑：问题 B、D、E；
   - 部署：静态资源，`templ generate`、`tailwindcss`、`shadcn-templ bundle` 三个构建步骤。

## 后续：改用框架 API（#124、#126）

`static/` 原型和 `app/flash.go` 中的 flash、HX-Trigger 原型已经进入框架，演示应用改用 `web.NewStatic`、`web.StaticURL`、`web.Flash`、`web.Flashes`、`web.Trigger`：

- 静态资源挂在 `/static/`，`components.json` 的 `scripts.path` 同步改为 `/static/js`；
- 保存、删除成功后直接返回 `204`，提示通过 `HX-Trigger` 送达，布局中一行监听器交给 `templ.toast.add`；
- 新增 F1 场景，验证视图文档「提示消息」一节中的 Alpine 写法。

全部场景通过。

## 后续：列表页改用框架 API（#134）

后台列表页改用 `web.ParseQuery`、`web.QueryURL`、`web.PushURL` 和 `page.Offset.Links`：

- 筛选表单移进列表区域，搜索框带 `hx-preserve`，问题 B 中的 `ListSync` 和 `hx-partial` 都不再需要；
- 新增两个场景：
  - L4：视图文档「列表页」的模板原样运行；去掉 `:inherited` 后失败，确认 htmx 4 的属性默认不继承；
  - S4：非法的查询参数返回 422 页面。

全部场景通过。

## 后续：开发命令改用框架的 dev（#140）

`cmd/dev` 原型已进入框架（`command/devcmd`）。PoC 改为 `cmd/poc`，由 `command.Root` 装配 `serve` 和 `dev`，配置放在 `config/app.yaml` 和 `config/local.yaml`。

真实运行的结果：
- 修改 `.templ` 文字、只改 CSS、修改 Go 代码后，浏览器都刷新，应用也重启了；
- 停止后所有子进程退出、端口释放；
- 组件脚本恢复为提交的版本，样式表与全新的 `--minify` 构建一致。

还发现 `shadcn-templ bundle --watch` 输出的是未压缩的脚本，文件名却不变，因此框架用 `devcmd.Tool{Watch, Build}` 在停止后重新生成正式版本。
