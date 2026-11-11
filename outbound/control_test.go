package outbound

import (
	"errors"
	"net/netip"
	"testing"
)

func TestControl(t *testing.T) {
	check := control(Options{})
	for _, tc := range []struct {
		network, address string
		allowed          bool
	}{
		{"tcp4", "93.184.215.14:443", true},
		{"tcp4", "93.184.215.14:80", true},
		{"tcp6", "[2606:2800:21f:cb07:6820:80da:af6b:8b2c]:443", true},
		{"tcp4", "93.184.215.14:8080", false}, // 默认只允许 80 和 443
		{"tcp4", "127.0.0.1:443", false},
		{"tcp4", "10.1.2.3:443", false},
		{"tcp4", "172.16.0.1:443", false},
		{"tcp4", "192.168.1.1:443", false},
		{"tcp4", "169.254.169.254:80", false}, // 云厂商元数据接口
		{"tcp4", "100.100.100.200:80", false}, // 阿里云元数据接口（运营商级 NAT 网段）
		{"tcp4", "0.0.0.0:80", false},
		{"tcp4", "224.0.0.1:80", false},
		{"tcp6", "[::1]:443", false},
		{"tcp6", "[fd00::1]:443", false},
		{"tcp6", "[fe80::1]:443", false},
		{"tcp6", "[::ffff:127.0.0.1]:443", false}, // IPv4 映射地址
		{"tcp6", "[64:ff9b::a00:1]:443", false},   // NAT64 内嵌 10.0.0.1
		{"tcp6", "[2002:a00:1::1]:443", false},    // 6to4 内嵌 10.0.0.1
		{"udp4", "93.184.215.14:443", false},
	} {
		err := check(tc.network, tc.address, nil)
		if tc.allowed && err != nil {
			t.Errorf("%s %s: %v", tc.network, tc.address, err)
		}
		if !tc.allowed && !errors.Is(err, ErrForbidden) {
			t.Errorf("%s %s allowed: %v", tc.network, tc.address, err)
		}
	}
}

func TestControlOptions(t *testing.T) {
	anyPort := control(Options{AnyPort: true})
	if err := anyPort("tcp4", "93.184.215.14:8080", nil); err != nil {
		t.Errorf("AnyPort: %v", err)
	}
	if err := anyPort("tcp4", "10.0.0.1:8080", nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("AnyPort still checks addresses: %v", err)
	}
	ports := control(Options{Ports: []uint16{8443}})
	if err := ports("tcp4", "93.184.215.14:8443", nil); err != nil {
		t.Errorf("Ports: %v", err)
	}
	if err := ports("tcp4", "93.184.215.14:443", nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("Ports replaces the defaults: %v", err)
	}
	allow := control(Options{AnyPort: true, Allow: []netip.Prefix{
		netip.MustParsePrefix("127.0.0.1/32"), netip.MustParsePrefix("::1/128"),
	}})
	if err := allow("tcp4", "127.0.0.1:8080", nil); err != nil {
		t.Errorf("Allow v4: %v", err)
	}
	if err := allow("tcp6", "[::1]:8080", nil); err != nil {
		t.Errorf("Allow v6: %v", err)
	}
	if err := allow("tcp4", "127.0.0.2:8080", nil); !errors.Is(err, ErrForbidden) {
		t.Errorf("Allow is exact: %v", err)
	}
}
