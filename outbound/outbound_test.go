package outbound_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/go-tenon/tenon/outbound"
)

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func ok(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }

// local 允许连接本机测试服务器。
var local = outbound.Options{AnyPort: true, Allow: []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}}

func TestRefusesPrivateAddresses(t *testing.T) {
	srv := server(t, ok)
	_, err := outbound.New(outbound.Options{AnyPort: true}).Get(srv.URL)
	if !errors.Is(err, outbound.ErrForbidden) {
		t.Fatalf("loopback: %v", err)
	}
	// 域名解析到内网地址同样被拒绝。
	_, err = outbound.New(outbound.Options{}).Get("http://localhost/")
	if !errors.Is(err, outbound.ErrForbidden) {
		t.Fatalf("localhost: %v", err)
	}
}

func TestAllow(t *testing.T) {
	srv := server(t, ok)
	resp, err := outbound.New(local).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// 放行地址不等于放行端口。
	_, err = outbound.New(outbound.Options{Allow: local.Allow}).Get(srv.URL)
	if !errors.Is(err, outbound.ErrForbidden) {
		t.Fatalf("port: %v", err)
	}
}

// 重定向建立的连接同样经过检查。
func TestRedirectToPrivateAddress(t *testing.T) {
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	_, err := outbound.New(local).Get(srv.URL)
	if !errors.Is(err, outbound.ErrForbidden) {
		t.Fatalf("redirect: %v", err)
	}
}

// 不使用环境变量中的代理：经过代理时检查的是代理的地址。
func TestIgnoresProxyEnvironment(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://192.0.2.1:3128")
	t.Setenv("HTTPS_PROXY", "http://192.0.2.1:3128")
	srv := server(t, ok)
	resp, err := outbound.New(local).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

func TestTimeout(t *testing.T) {
	if c := outbound.New(outbound.Options{}); c.Timeout != outbound.DefaultTimeout {
		t.Errorf("default timeout %v", c.Timeout)
	}
	if c := outbound.New(outbound.Options{Timeout: -1}); c.Timeout != 0 {
		t.Errorf("negative timeout %v", c.Timeout)
	}
	if c := outbound.New(outbound.Options{Timeout: time.Second}); c.Timeout != time.Second {
		t.Errorf("timeout %v", c.Timeout)
	}
}
