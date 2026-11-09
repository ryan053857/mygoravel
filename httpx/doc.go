// Package httpx 提供 HTTP 配置、默认中间件与服务器停机协调。
//
// Config 描述监听地址、服务器超时、请求限额与可信代理；
// ListenAndServe 按 Config 启动服务器，并在 ctx 取消时排空请求后退出。
package httpx
