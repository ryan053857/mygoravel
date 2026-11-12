package oauthserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/cockroachdb/errors"
)

// maxMetadata 是取回的客户端元数据文档的最大字节数。
const maxMetadata = 64 << 10

// Client 是预先登记的客户端，例如自研的 agent。
type Client struct {
	// ID 是客户端 ID，不能是 https 地址（https 地址的 client_id 按 Client ID Metadata Document 处理）。
	ID string
	// Name 显示在同意页上。
	Name string
	// RedirectURIs 是允许的回调地址，按完整地址匹配；本机地址（localhost、127.0.0.1、[::1]）忽略端口。
	RedirectURIs []string
}

// client 是一次授权中确定的客户端。
type client struct {
	id           string
	name         string
	redirectURIs []string
}

// clientMetadata 是 Client ID Metadata Document 中本包使用的字段。
type clientMetadata struct {
	ClientID                string   `json:"client_id"`
	ClientName              string   `json:"client_name"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// resolve 返回 id 对应的客户端：预先登记的客户端，或者从 https 地址取回 Client ID Metadata Document。
func (s *Server) resolve(ctx context.Context, id string) (*client, error) {
	if c, ok := s.clients[id]; ok {
		return &client{id: c.ID, name: c.Name, redirectURIs: c.RedirectURIs}, nil
	}
	if s.opts.DynamicRegistration && strings.HasPrefix(id, registeredPrefix) {
		c, err := s.registered(ctx, id)
		if err != nil || c != nil {
			return c, err
		}
		return nil, errors.Newf("unknown client %q", id)
	}
	if !metadataURL(id) {
		return nil, errors.Newf("unknown client %q", id)
	}
	return s.fetchMetadata(ctx, id)
}

// metadataURL 报告 id 能否作为 Client ID Metadata Document 的地址：https、带路径、没有片段和用户信息。
func metadataURL(id string) bool {
	u, err := url.Parse(id)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.Path != "" && u.Path != "/" &&
		u.Fragment == "" && u.User == nil
}

// fetchMetadata 取回 id 上的客户端元数据文档，检查其中的 client_id 与地址完全相同。
// 用 Options.HTTPClient（默认拒绝内网地址）取回，每次授权都重新取，不缓存。
func (s *Server) fetchMetadata(ctx context.Context, id string) (*client, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, id, nil)
	if err != nil {
		return nil, errors.Wrap(err, "client metadata")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, errors.Wrap(err, "fetch client metadata")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Newf("fetch client metadata: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil {
		return nil, errors.Wrap(err, "read client metadata")
	}
	if len(data) > maxMetadata {
		return nil, errors.New("client metadata is too large")
	}
	var m clientMetadata
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, errors.Wrap(err, "decode client metadata")
	}
	switch {
	case m.ClientID != id:
		return nil, errors.Newf("client metadata client_id %q does not match %q", m.ClientID, id)
	case len(m.RedirectURIs) == 0:
		return nil, errors.New("client metadata has no redirect_uris")
	case m.TokenEndpointAuthMethod != "" && m.TokenEndpointAuthMethod != "none":
		return nil, errors.Newf("token_endpoint_auth_method %q is not supported; only public clients (none) are", m.TokenEndpointAuthMethod)
	}
	name := strings.TrimSpace(m.ClientName)
	if name == "" {
		u, _ := url.Parse(id)
		name = u.Host
	}
	return &client{id: id, name: name, redirectURIs: m.RedirectURIs}, nil
}

// allows 报告 redirect 是否是客户端登记的回调地址之一：完整地址相同；本机的 http 地址忽略端口，
// 例如登记了 http://localhost/callback 时，http://localhost:3118/callback 也可以。
func (c *client) allows(redirect string) bool {
	if slices.Contains(c.redirectURIs, redirect) {
		return true
	}
	got, err := url.Parse(redirect)
	if err != nil || got.Scheme != "http" || !loopback(got.Hostname()) {
		return false
	}
	for _, r := range c.redirectURIs {
		want, err := url.Parse(r)
		if err == nil && want.Scheme == "http" && want.Hostname() == got.Hostname() &&
			want.Path == got.Path && want.RawQuery == got.RawQuery {
			return true
		}
	}
	return false
}

func loopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}
