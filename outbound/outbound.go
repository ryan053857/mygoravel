// Package outbound 创建只能访问公网地址的 HTTP 客户端，用于请求由用户或租户填写的地址，
// 例如 webhook 端点、网页抓取和自定义工具，防止服务端请求伪造（SSRF）：
// 有人填写 http://169.254.169.254/、http://10.0.0.5/ 这类地址，借服务器访问云厂商的元数据接口或内网服务。
//
//	client := outbound.New(outbound.Options{Timeout: 10 * time.Second})
//	resp, err := client.Get(url)
//	if errors.Is(err, outbound.ErrForbidden) { ... } // 目标是内网地址，重试也没有用
//
// 地址在域名解析之后、建立连接之前检查（net.Dialer.Control），检查的是真正要连接的 IP：
// 域名临时解析到内网地址（DNS 重绑定）、重定向到内网地址都会被拒绝。
// 拒绝的地址取自 IANA 的特殊用途地址登记表，由 code.dny.dev/ssrf 维护，包括内网、回环、链路本地、
// 运营商级 NAT（100.64.0.0/10，阿里云的元数据接口 100.100.100.200 在其中）、组播、保留地址，
// 以及可以内嵌 IPv4 地址的 NAT64、6to4 等 IPv6 前缀。
package outbound

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"syscall"
	"time"

	"code.dny.dev/ssrf"
)

// ErrForbidden 表示目标地址不允许访问：内网、回环等特殊地址，或者端口不在允许的范围内。
// 请求返回的错误可以用 errors.Is 识别它，重试也不会成功。
var ErrForbidden = errors.New("outbound: destination not allowed")

// DefaultTimeout 是 Options.Timeout 为零时整个请求的超时，包括读取响应体。
const DefaultTimeout = 30 * time.Second

// Options 是 New 的选项，零值可用。
type Options struct {
	// Timeout 是整个请求的超时，包括连接、重定向和读取响应体；零值为 DefaultTimeout，负数表示不限制。
	Timeout time.Duration
	// Ports 是允许连接的端口，nil 时只允许 80 和 443。
	Ports []uint16
	// AnyPort 允许连接任何端口，Ports 被忽略。地址检查仍然生效。
	AnyPort bool
	// Allow 是额外允许的网段，先于内置的拒绝规则检查。用于本地开发（127.0.0.1/32、::1/128），
	// 或者有意访问的内部服务。
	Allow []netip.Prefix
}

// New 返回只能访问公网地址的 HTTP 客户端。连接参数与 http.DefaultTransport 相同（包括 HTTP/2），
// 只是在建立每个连接之前检查地址，并且不使用代理：经过 HTTP_PROXY 等代理时，连接的是代理的地址，
// 无法检查真正的目标。需要经代理访问外网的部署，请在代理上做同样的限制。
//
// 客户端跟随重定向（最多 10 次），每次重定向建立的连接同样经过检查；不需要跟随时设置 CheckRedirect。
func New(opts Options) *http.Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second, Control: control(opts)}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	timeout := opts.Timeout
	switch {
	case timeout == 0:
		timeout = DefaultTimeout
	case timeout < 0:
		timeout = 0
	}
	return &http.Client{Transport: transport, Timeout: timeout}
}

// control 返回检查连接地址的 net.Dialer.Control。
func control(opts Options) func(network, address string, c syscall.RawConn) error {
	var guard []ssrf.Option
	switch {
	case opts.AnyPort:
		guard = append(guard, ssrf.WithAnyPort())
	case opts.Ports != nil:
		guard = append(guard, ssrf.WithPorts(opts.Ports...))
	}
	var v4, v6 []netip.Prefix
	for _, p := range opts.Allow {
		if p.Addr().Is4() {
			v4 = append(v4, p)
		} else {
			v6 = append(v6, p)
		}
	}
	if v4 != nil {
		guard = append(guard, ssrf.WithAllowedV4Prefixes(v4...))
	}
	if v6 != nil {
		guard = append(guard, ssrf.WithAllowedV6Prefixes(v6...))
	}
	g := ssrf.New(guard...)
	return func(network, address string, c syscall.RawConn) error {
		if err := g.Safe(network, address, c); err != nil {
			return fmt.Errorf("%w: %w", ErrForbidden, err)
		}
		return nil
	}
}
