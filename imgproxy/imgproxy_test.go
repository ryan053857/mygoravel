package imgproxy_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/ryan053857/mygoravel/imgproxy"
)

// 密钥和盐取自 imgproxy 文档的签名示例；TestURLSigned 的签名已用 imgproxy 容器验证。
const (
	docKey  = "943b421c9eb07c830af81030552c86009268de4e532ba2ee2eab8247c6da0881"
	docSalt = "520f986b998545b4785e0defbc4f3c1203f22de2374a3d53cb7a7fe9fea309c5"
)

func TestURL(t *testing.T) {
	p, err := imgproxy.New(imgproxy.Config{URL: "https://img.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	src := "https://cdn.example.com/2026/11/12/a.jpg"
	enc := base64.RawURLEncoding.EncodeToString([]byte(src))
	tests := []struct {
		name string
		o    imgproxy.Options
		want string
	}{
		{"no resize", imgproxy.Options{}, "/insecure/" + enc},
		{"default fit", imgproxy.Options{Width: 400}, "/insecure/rs:fit:400:0/" + enc},
		{"fill with format", imgproxy.Options{Width: 300, Height: 200, Resize: imgproxy.Fill, Format: "webp"}, "/insecure/rs:fill:300:200/" + enc + ".webp"},
	}
	for _, tt := range tests {
		if got := p.URL(src, tt.o); got != "https://img.example.com"+tt.want {
			t.Errorf("%s: got %s, want %s", tt.name, got, tt.want)
		}
	}
}

func TestURLSigned(t *testing.T) {
	p, err := imgproxy.New(imgproxy.Config{URL: "https://img.example.com", Key: docKey, Salt: docSalt})
	if err != nil {
		t.Fatal(err)
	}
	got := p.URL("http://example.com/images/curiosity.jpg", imgproxy.Options{Width: 300, Height: 400, Resize: imgproxy.Fill, Format: "png"})
	want := "https://img.example.com/Mto5k53p2heDeA6oPr-mF7qv5jjK55AL-oueHaLDDBE/rs:fill:300:400/aHR0cDovL2V4YW1wbGUuY29tL2ltYWdlcy9jdXJpb3NpdHkuanBn.png"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestPassthrough(t *testing.T) {
	p, err := imgproxy.New(imgproxy.Config{})
	if err != nil {
		t.Fatal(err)
	}
	thumb := imgproxy.Options{Width: 80, Height: 80, Resize: imgproxy.Fill}
	if got := p.URL("/storage/a.png", thumb); got != "/storage/a.png" {
		t.Errorf("unconfigured: got %s", got)
	}
	var nilProxy *imgproxy.Proxy
	if got := nilProxy.URL("/storage/a.png", thumb); got != "/storage/a.png" {
		t.Errorf("nil proxy: got %s", got)
	}
	configured, _ := imgproxy.New(imgproxy.Config{URL: "https://img.example.com"})
	if got := configured.URL("", thumb); got != "" {
		t.Errorf("empty src: got %s", got)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		cfg  imgproxy.Config
		want string
	}{
		{imgproxy.Config{}, ""},
		{imgproxy.Config{URL: "https://img.example.com", Key: docKey, Salt: docSalt}, ""},
		{imgproxy.Config{URL: "img.example.com"}, "must be an http or https address"},
		{imgproxy.Config{URL: "https://img.example.com", Key: docKey}, "key and salt must be set together"},
		{imgproxy.Config{URL: "https://img.example.com", Key: "secret", Salt: docSalt}, "key must be hex encoded"},
	}
	for _, tt := range tests {
		err := tt.cfg.Validate()
		if tt.want == "" && err != nil {
			t.Errorf("%+v: unexpected error %v", tt.cfg, err)
		}
		if tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)) {
			t.Errorf("%+v: got %v, want %q", tt.cfg, err, tt.want)
		}
	}
}
