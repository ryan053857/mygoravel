// Package httpx 提供 HTTP 配置、可组合的中间件与服务器停机协调。
//
// Config 描述服务器参数与各中间件的参数，配置不决定中间件是否启用；
// Defaults 按 Config 返回基础中间件栈，其中每个中间件也可单独使用，
// RateLimit 等可选中间件由应用按需安装；
// ListenAndServe 按 Config 启动服务器，并在 ctx 取消时排空请求后退出。
// SPA 提供嵌入二进制的单页应用，开发时转发给前端的开发服务器。
package httpx
