# 结果与结论

环境：Go 1.27.3、templ v0.3.1100、shadcn-templ v2.0.0、Tailwind CSS v4.3.3 standalone、htmx 4.0.1（框架内嵌）、Chrome（chromedp v0.23.0 headless）。

## 场景

| # | 场景 | 结果 |
|---|---|---|
| S1 | 两个区域各自的登录跳转、风格 class、样式表；每页只有一份样式表；租户端的主题变量输出为 `<style>`；登录运营后台不会登录租户端；404 按区域呈现 | ✅ |
| S2 | 运营后台视图中的类名都在预编译样式表中 | ✅ 加上常用工具类之后；之前缺 `px-6 py-3 text-xl text-right`，见发现 4 |
| B1 | 运营后台登录：验证失败在字段旁显示错误（`webui.TextField`，`aria-invalid`），成功后回到原地址 | ✅ |
| B2 | 列表：搜索、`webui.Pagination` 只替换列表区域，地址栏同步 | ✅ |
| B3 | 对话框新建：422 在对话框内回显；成功后 `webui.CloseDialog` 关闭、列表刷新、toast | ✅ |
| B4 | 删除确认框；局部请求的错误由 `webui.ErrorFragment` 显示为 toast | ✅ |
| B5 | 暗色模式切换，刷新后保留 | ✅ |
| B6 | 租户端：maia 风格，`--primary` 是品牌色，输入框圆角与运营后台不同（41.6px 对 10px）；按钮的 `Class: "rounded-none bg-brand-600"` 覆盖风格 | ✅ |
| B7 | 租户端登录后，web.Flash 显示在自己的 Alpine 提示条中，页面上没有 shadcn toast；htmx 请求带来的提示同样显示 | ✅ |
| B8 | 租户端局部请求的错误显示为自己的错误横幅 | ✅ |

截图：运营后台列表、422 对话框、toast、暗色；租户端登录页、首页和提示条。

## 体积

| | 原始 | gzip |
|---|---|---|
| 全部组件脚本（70 多个组件） | 387 KB | 120 KB |
| 每套风格的预编译样式表（组件 + 风格 + 默认主题 + 常用工具类） | 约 450 KB | 约 48 KB |
| 其中常用工具类 | 约 106 KB | 约 12 KB |
| 租户端用 Tailwind 生成的样式表（组件 + maia + 自己的类名） | 343 KB | 37 KB |

8 套风格放进一份样式表时为 2.27 MB（gzip 184 KB），所以每套风格单独生成。

组件脚本中 `baseui` 是共用基础（源码 230 KB），其余较大的是 chart（99 KB）、floatingui（78 KB）、drawer、resizable、calendar（各约 50 KB）。

## 发现

**1. 直接依赖固定版本可行。**
- 组件是普通 Go 包，生成的 `_templ.go` 随源码提交，应用和框架都直接 import，不需要 shadcn-templ CLI。
- 风格是纯 CSS：组件只带 `cn-*` 语义类，`.style-<风格> .cn-*` 决定外观，`<html class="style-maia">` 选择风格。
- 风格以 `layer(base)` 导入（与上游文档站相同），低于工具类，所以组件 `Class` 中的工具类总能覆盖风格（B6）。
- 升级流程：改 go.mod 中的版本 → `go run ./gen` → 跑浏览器场景 → 检查 `css/vendor` 的 diff。

**2. 不能 import 根包 `components`。**
它有 `//go:embed all:*`，会把全部组件源码打进二进制；它的 `Scripts()` 也指向 `/assets/js/`。框架用自己的 `Head` 输出脚本。

**3. 上游不提供构建产物，框架用 `go:generate` 生成。**
- JS：与 `shadcn-templ bundle` 相同，baseui 在前，逐个用 esbuild 压缩后拼接，约 120 行代码；
- CSS：每套风格一份，只有框架维护者需要 Tailwind；
- `components.txt`：从组件源码提取的类名候选。Tailwind 扫描它而不是模块缓存中的组件源码，所以应用的 Tailwind 不需要知道 shadcn-templ 的路径，只需要框架的路径（发现 7）；
- 上游的 `style-classes.txt` 不需要：它是文档站内联预览用的，风格中的 `@apply` 本身就会生成样式。去掉后样式表小了约 40%。

**4. 不装 Tailwind 的区域只能用预编译样式表中已有的类名。**
组件用到的类名之外，页面布局常用的 `px-6`、`text-xl`、`text-right` 并不存在。预编译样式表因此附带一组常用工具类（`css/safelist.css`：间距、flex、grid、尺寸、文字、主题颜色，断点 sm、md、lg），gzip 后多 12 KB。超出这组类名的页面需要 Tailwind。框架可以提供与 S2 相同的检查，在测试中列出不存在的类名。

**5. 区域的划分不需要框架有新的概念。**
- 每个区域一个 `web.Pages`（会话 cookie、登录页、错误呈现），一个 chi 子路由（404 用 `r.NotFound(pages.NotFound)`）。
- 主题放进 context（`webui.Use`），`Page`、`Head`、错误页从 context 读取，视图不需要传主题。框架化后应当是 `web.Options.Theme`：Pages 已经为每条路由和错误页设置 context（与 `Options.Static` 相同），不需要另装中间件。
- `Theme.Stylesheet` 让使用 Tailwind 的区域换成自己的样式表，其余字段（风格、模式、圆角、字体、变量）都可以写在 YAML 中，并有 `Validate`。

**6. 前端衔接都是可选的。**
- 运营后台用 `webui.Toaster()`（shadcn toast），租户端用自己的 Alpine 提示条。两者都消费框架的 `web:flash`，衔接脚本只在页面有 Toaster 时把提示交给 toast。
- 错误片段：运营后台用 `webui.ErrorFragment`（toast），租户端用自己的横幅。
- 自己写 `<html>` 的区域只需要 `class={ webui.ThemeFrom(ctx).Class() }` 和 `@webui.Head()`。

**7. 需要框架配合的地方。**

a. **`web.Options.Login` 是固定字符串。** 租户在路径中时（`/t/{tenant}/login`）无法表达。建议改为 `func(*http.Request) string`，或者允许 `{tenant}` 占位。

b. **局部请求的错误。** 计划中的「默认通过 `web:flash` 送达」与发现 6 一致：默认交给提示组件；需要不同呈现的区域仍然可以设置 `ErrorFragment`。设置后 `ErrorTarget` 才是必填的。

c. **应用 Tailwind 导入框架样式的路径。** PoC 中 webui 在同一个模块里，用相对路径导入。真实项目中框架在模块缓存里，路径因机器而异，不能写进 `app.css`。建议由 `devcmd.Tailwind` 用 `go list -m -f {{.Dir}}` 找到框架目录，生成一个包装入口：先导入 `tailwindcss` 和框架的风格入口，再导入应用的 `app.css`。应用的 `app.css` 只写自己的令牌和样式。这一步还要在 PR 中验证。

d. **脚本体积。** 全部组件的脚本 gzip 后 120 KB，比之前应用只复制 22 个组件时的 45 KB 大很多。建议分为默认包（baseui 和常用组件）和按需加载的扩展（chart、calendar、drawer、resizable 等），在 `Theme` 或 `Head` 的参数中启用。

e. **基础色。** shadcn 的主题还有基础色（neutral、stone、zinc、gray、slate）。PoC 只内置 neutral，其余颜色只能用 `vars` 覆盖。可以把 CLI 中的色板做成 `Theme.Base`。

f. **字体文件。** `Theme.Font` 只是 font-family，字体文件由应用作为静态文件提供。

## 对框架的结论

框架依赖固定版本的 shadcn-templ，加上 webui 这一层衔接，能做到不装 Tailwind、不跑 CLI、不写 `app.js` 就有完整的后台界面。同时不同区域可以有自己的风格、主题、登录页、提示和错误样式，也可以用自己的 Tailwind 做设计感强的页面。

建议的 PR：

1. **`web/ui`**：
   - 生成器和嵌入的资源（脚本分为默认包和扩展，见 7d）；
   - `Theme` 和 `web.Options.Theme`；
   - `Head`、`Page`、`Toaster`、`Modal`、`ErrorPage`、`web.CloseDialog`、衔接脚本；
   - 局部请求的错误默认走 `web:flash`（7b）；
   - `Options.Login` 支持按请求生成（7a）。
2. **表单和分页组件**：`TextField`、`TextareaField`、`Pagination`，以及 select、checkbox 等。
3. **应用的 Tailwind 接入**：`devcmd.Tailwind` 解析框架路径（7c）；重写视图、界面组件、前端资源三篇文档；提供不装 Tailwind 时检查类名的辅助（发现 4）。
