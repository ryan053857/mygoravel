package mcpx

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/cockroachdb/errors"
)

// Resource 描述 MCP 服务作为 OAuth 受保护资源的信息（RFC 9728）。claude.ai、ChatGPT 的远程连接器据此找到授权服务器，
// 让用户在浏览器中登录并同意授权，见 oauthserver。
type Resource struct {
	// URL 是 MCP 服务的规范地址，例如 https://blog.example.com/mcp，必须与用户在客户端中填写的完全相同。
	// 授权服务器签发的令牌只能用于这个地址。
	URL string
	// AuthorizationServers 是授权服务器的 issuer，第一个是主要的。
	AuthorizationServers []string
	// Scopes 是客户端可以申请的范围。
	Scopes []string
}

// Validate 检查 URL 是带路径的 https 地址（本机地址可以用 http），且至少有一个授权服务器。
func (r Resource) Validate() error {
	u, err := url.Parse(r.URL)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback(u.Hostname()))) {
		return errors.Newf("mcpx: resource URL %q must be an absolute https URL without query or fragment", r.URL)
	}
	if len(r.AuthorizationServers) == 0 {
		return errors.New("mcpx: resource needs an authorization server")
	}
	return nil
}

// MetadataURL 返回受保护资源元数据的地址：在 URL 的路径前插入 /.well-known/oauth-protected-resource，
// 例如 https://blog.example.com/mcp 得到 https://blog.example.com/.well-known/oauth-protected-resource/mcp。
// 把 ResourceMetadata 挂在这个地址的路径上。
func (r Resource) MetadataURL() string {
	u, err := url.Parse(r.URL)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host + "/.well-known/oauth-protected-resource" + strings.TrimSuffix(u.Path, "/")
}

// MetadataPath 返回 MetadataURL 的路径部分，用于注册路由。
func (r Resource) MetadataPath() string {
	u, err := url.Parse(r.MetadataURL())
	if err != nil {
		return ""
	}
	return u.Path
}

// ResourceMetadata 返回受保护资源元数据（RFC 9728）的处理器，挂在 r.MetadataPath() 上：
//
//	router.Handle(res.MetadataPath(), mcpx.ResourceMetadata(res))
//
// r 不合法时 panic。
func ResourceMetadata(r Resource) http.Handler {
	if err := r.Validate(); err != nil {
		panic(err)
	}
	body, err := json.Marshal(map[string]any{
		"resource":                 r.URL,
		"authorization_servers":    r.AuthorizationServers,
		"scopes_supported":         r.Scopes,
		"bearer_methods_supported": []string{"header"},
	})
	if err != nil {
		panic(errors.Wrap(err, "mcpx: encode resource metadata"))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write(body)
	})
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
