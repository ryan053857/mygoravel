# 框架内置 shadcn-templ 的 PoC

验证框架直接依赖固定版本的 [shadcn-templ](https://github.com/axadrn/shadcn-templ)（v2.0.0），不再由应用复制组件：

- `webui/` 模拟将来框架中的 `web/ui`：
  - `gen/`：从 go.mod 中的 shadcn-templ 生成组件脚本、每套风格的样式表和 Tailwind 扫描用的类名，`cd webui && go run ./gen`（需要 tailwindcss）；
  - `css/ui.css`：框架的样式源，`css/style-<风格>.css` 是生成的风格入口，`css/safelist.css` 是预编译样式表附带的常用工具类；
  - `theme.go`：可以写在 YAML 中的 `Theme`，`Use` 把它设为区域主题；
  - `head.go`、`ui.templ`：`Head`、`Page`、`Toaster`、`Modal`、`ErrorPage`、`ErrorFragment`，以及绑定 `web.Form`、`page.Offset` 的 `TextField`、`TextareaField`、`Pagination`；
  - `assets/glue.js`：把 `web:flash`、`web:dialog-close` 交给组件。
- `app/`：同一个二进制中的两个区域：
  - 运营后台 `/ops`：nova 风格，只用框架预编译的样式表，shadcn toast，列表、对话框新建、删除确认；
  - 租户端 `/app`：maia 风格、品牌主色、大圆角，自己的 Tailwind 样式表（`app/css/app.css` 导入框架的风格入口），自己写 `<html>`，Alpine 写的提示条和错误横幅。
  - 两个区域各有 `web.Pages`、会话 cookie、登录页和 404。
- `config/app.yaml`：两个区域的主题。

## 运行

```bash
go run ./cmd/poc serve --config config/app.yaml   # http://localhost:8080/ops/articles（ops@example.com / secret）
                                                  # http://localhost:8080/app/（alice@acme.test / secret）
```

## 构建

```bash
(cd webui && go run ./gen)                                           # 升级 shadcn-templ 或修改 css/ui.css 后
go tool templ generate
tailwindcss -i app/css/app.css -o app/static/css/app.css --minify   # 租户端的样式表
```

## 测试

```bash
go test -count=1 .                                       # 服务端场景
POC_BROWSER=1 go test -count=1 -run TestBrowser -v .     # 浏览器场景，需要 /usr/bin/google-chrome
```

## 结论

见 [results.md](results.md)。
