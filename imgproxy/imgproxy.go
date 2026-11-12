// Package imgproxy 生成 imgproxy 的图片地址：把文件的公开地址和缩放参数拼成一个签名的 URL，
// 由 imgproxy 按需缩放、转换格式，不保存额外的文件。
//
//	images, err := imgproxy.New(cfg.Imgproxy)
//	thumb := images.URL(file.URL, imgproxy.Options{Width: 300, Height: 200, Resize: imgproxy.Fill})
//
// 没有配置 url 时（例如开发环境），URL 原样返回原图地址。
package imgproxy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"

	"github.com/cockroachdb/errors"
)

// Config 是 imgproxy 配置段。
//
//	imgproxy:
//	  url: https://img.example.com
//	  key: ${IMGPROXY_KEY}
//	  salt: ${IMGPROXY_SALT}
type Config struct {
	// URL 是 imgproxy 的公开地址。为空时不经过 imgproxy，URL 返回原图地址。
	URL string `yaml:"url"`
	// Key 和 Salt 是签名用的密钥和盐，十六进制，与 imgproxy 的 IMGPROXY_KEY、IMGPROXY_SALT 相同。
	// 为空时生成不签名的地址（insecure），只适合开发环境。
	Key  string `yaml:"key"`
	Salt string `yaml:"salt"`
}

// Validate 检查 url 是 http(s) 地址，key 和 salt 是十六进制并且同时填写。
func (c Config) Validate() error {
	var errs []error
	if c.URL != "" {
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
			errs = append(errs, errors.Newf("url %q must be an http or https address", c.URL))
		}
	}
	if (c.Key == "") != (c.Salt == "") {
		errs = append(errs, errors.New("key and salt must be set together"))
	}
	if _, err := hex.DecodeString(c.Key); err != nil {
		errs = append(errs, errors.New("key must be hex encoded"))
	}
	if _, err := hex.DecodeString(c.Salt); err != nil {
		errs = append(errs, errors.New("salt must be hex encoded"))
	}
	return errors.Join(errs...)
}

// Resize 是缩放方式，对应 imgproxy 的 resizing_type。
type Resize string

const (
	// Fit 保持比例缩放到 Width×Height 以内，是默认方式。
	Fit Resize = "fit"
	// Fill 保持比例缩放并裁掉多余部分，结果正好是 Width×Height，适合头像和卡片封面。
	Fill Resize = "fill"
)

// Options 是一种图片尺寸。通常在代码中声明为变量，在各处复用：
//
//	var cardCover = imgproxy.Options{Width: 600, Height: 400, Resize: imgproxy.Fill, Format: "webp"}
type Options struct {
	// Width、Height 是目标宽高（像素），0 表示按另一边等比计算；都为 0 时不缩放。
	Width, Height int
	// Resize 是缩放方式，默认 Fit。图片小于目标尺寸时不放大。
	Resize Resize
	// Format 是输出格式，例如 webp、avif、jpg；为空时保持原格式。
	Format string
}

// Proxy 生成 imgproxy 地址，可以并发使用。
type Proxy struct {
	base      string
	key, salt []byte
}

// New 按配置创建 Proxy，不访问网络。
func New(cfg Config) (*Proxy, error) {
	if err := cfg.Validate(); err != nil {
		return nil, errors.Wrap(err, "imgproxy")
	}
	key, _ := hex.DecodeString(cfg.Key)
	salt, _ := hex.DecodeString(cfg.Salt)
	return &Proxy{base: strings.TrimRight(cfg.URL, "/"), key: key, salt: salt}, nil
}

// URL 返回 src 按 o 处理后的图片地址。src 是图片的公开地址，通常是 filesystem.File 的 URL；
// 以 / 开头的相对地址由 imgproxy 的 IMGPROXY_BASE_URL 补全。src 为空或没有配置 url 时原样返回 src。
func (p *Proxy) URL(src string, o Options) string {
	if p == nil || p.base == "" || src == "" {
		return src
	}
	var path strings.Builder
	if o.Width > 0 || o.Height > 0 {
		resize := o.Resize
		if resize == "" {
			resize = Fit
		}
		path.WriteString("/rs:" + string(resize) + ":" + strconv.Itoa(max(o.Width, 0)) + ":" + strconv.Itoa(max(o.Height, 0)))
	}
	path.WriteString("/" + base64.RawURLEncoding.EncodeToString([]byte(src)))
	if o.Format != "" {
		path.WriteString("." + o.Format)
	}
	return p.base + "/" + p.sign(path.String()) + path.String()
}

// sign 返回路径的签名：HMAC-SHA256(key, salt + path)，URL 安全的 Base64；没有密钥时是 insecure。
func (p *Proxy) sign(path string) string {
	if len(p.key) == 0 {
		return "insecure"
	}
	mac := hmac.New(sha256.New, p.key)
	mac.Write(p.salt)
	mac.Write([]byte(path))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
