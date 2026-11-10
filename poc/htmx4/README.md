# templ + htmx 4 + Alpine PoC

验证 mygoravel 的服务端渲染形态：templ 渲染页面，htmx 4（4.0.1）负责局部更新和 SSE，Alpine.js（3.17.5）负责前端状态。独立 module，不影响框架 go.mod。

- `web/`：`httpx/web` 的原型——渲染、错误映射、表单解码与验证、SSE 流、排空、页面认证、内嵌脚本。
- `app/`：演示应用——登录、文章编辑表单、两人聊天室（输入状态、断线补发）、流式任务。
- `cmd/demo`：`go run ./cmd/demo`，访问 http://localhost:8080，任意邮箱 + 密码 `secret`；`admin@example.com` 有 admin scope。

## 运行测试

```bash
go tool templ generate                      # 修改 .templ 后
go test -count=1 ./...                      # 表单单元测试 + 服务端场景（S1–S5）
POC_BROWSER=1 go test -count=1 -run TestBrowser -v .   # 浏览器场景（B1–B9），需要 /usr/bin/google-chrome
```

nginx 缓冲验证是手动的，见 [results.md](results.md#nginx)。

## 结论

见 [results.md](results.md)。
