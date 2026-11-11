# shadcn-templ + Tailwind PoC

验证 mygoravel 页面的 UI 层：后台用 [shadcn-templ](https://github.com/axadrn/shadcn-templ) 组件，面向用户的页面用同一套设计令牌加 Tailwind 工具类。直接使用框架的 `web`、`session`、`httpx`，独立 module，不影响框架 go.mod。

- `components/`、`utils/`、`assets/css/{shadcn-tailwind,tw-animate}.css`：`shadcn-templ init --preset nova` 和 `shadcn-templ add …` 复制进来的代码（v2.0.0，22 个组件），未修改。
- `assets/css/globals.css`：Tailwind 入口。shadcn 的主题变量之后是应用自己的品牌令牌（`--color-brand-*`、`--font-display`）和 View Transition 动画。
- `static/`：「应用静态资源」的原型。按内容哈希加指纹，可以长期缓存；开发模式直接读磁盘。
- `app/`：演示应用。
  - 后台 `/admin/articles`：筛选、排序、翻页，对话框里新建和编辑，下拉菜单，删除确认，toast。
  - 落地页 `/`：品牌色、暗色模式、价格切换的 View Transition、普通表单和跳转后的 flash。
  - `app/flash.go`：flash 和 HX-Trigger 辅助函数的原型。
- `cmd/demo`：`go run ./cmd/demo`，访问 http://localhost:8080。
- `cmd/dev`：开发命令原型，用 `command.Concurrently` 同时运行 tailwind、shadcn-templ bundle 和 templ 三个 watch 进程。

## 构建

```bash
go tool templ generate
tailwindcss -i assets/css/globals.css -o assets/css/app.css --minify   # Tailwind v4 standalone CLI
shadcn-templ bundle                                                     # 修改组件 JS 后
go run ./cmd/dev                                                        # 开发：打开 http://localhost:7331
```

## 测试

```bash
go test -count=1 .                                          # 服务端场景 S1–S3
POC_BROWSER=1 go test -count=1 -run TestBrowser -v .        # 浏览器场景，需要 /usr/bin/google-chrome
SHOTS=/some/dir POC_BROWSER=1 go test -run TestBrowser .    # 同时保存截图
```

本机没有中文字体时，截图里的中文显示为空白。可以只给测试用的 Chrome 指定一个带 Noto Sans SC 的 `FONTCONFIG_FILE`。

## 结论

见 [results.md](results.md)。
