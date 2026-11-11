package crypt

import (
	"crypto/hmac"
	"crypto/sha256"
	"net/url"
	"strconv"
	"time"

	"github.com/ryan053857/mygoravel/errorx"
)

var (
	// ErrInvalidSignature 表示链接没有签名、签名不对，或者签名之后被改过。
	ErrInvalidSignature = errorx.Define(errorx.PermissionDenied, "url.invalid_signature")
	// ErrExpired 表示链接的签名正确，但已经过了有效期。
	ErrExpired = errorx.Define(errorx.PermissionDenied, "url.expired")
)

// SignURL 给 rawURL 加上 expires（ttl 后过期的 Unix 秒）和 signature 两个查询参数。
// 签名覆盖路径和其余全部查询参数，不覆盖协议和域名。ttl 为 0 表示永不过期，这时不加 expires。
// rawURL 中已有的 expires、signature 参数被替换。rawURL 无法解析或 ttl 为负数时 panic。
func (c *Crypt) SignURL(rawURL string, ttl time.Duration) string {
	if ttl < 0 {
		panic("crypt: SignURL: negative ttl")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		panic("crypt: SignURL: " + err.Error())
	}
	q := u.Query()
	q.Del("signature")
	q.Del("expires")
	if ttl > 0 {
		q.Set("expires", strconv.FormatInt(time.Now().Add(ttl).Unix(), 10))
	}
	q.Set("signature", b64.EncodeToString(mac(c.macKeys[0], u, q)))
	u.RawQuery = q.Encode()
	return u.String()
}

// VerifyURL 校验 SignURL 生成的链接，页面处理器传入 r.URL。签名不对时返回 ErrInvalidSignature，
// 过期时返回 ErrExpired，两者都是 errorx.PermissionDenied。签名依次用当前密钥和 PreviousKeys 校验。
func (c *Crypt) VerifyURL(u *url.URL) error {
	q := u.Query()
	sigs := q["signature"]
	if len(sigs) != 1 {
		return ErrInvalidSignature.New()
	}
	sig, err := b64.DecodeString(sigs[0])
	if err != nil {
		return ErrInvalidSignature.New()
	}
	q.Del("signature")
	if !c.validMAC(sig, u, q) {
		return ErrInvalidSignature.New()
	}
	if exp := q["expires"]; len(exp) > 0 {
		at, err := strconv.ParseInt(exp[0], 10, 64)
		if err != nil || len(exp) != 1 {
			return ErrInvalidSignature.New()
		}
		if time.Now().Unix() >= at {
			return ErrExpired.New()
		}
	}
	return nil
}

func (c *Crypt) validMAC(sig []byte, u *url.URL, q url.Values) bool {
	for _, key := range c.macKeys {
		if hmac.Equal(sig, mac(key, u, q)) {
			return true
		}
	}
	return false
}

// mac 计算路径与查询参数（不含 signature，按键排序）的 HMAC-SHA256。
func mac(key []byte, u *url.URL, q url.Values) []byte {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	h := hmac.New(sha256.New, key)
	h.Write([]byte(path + "?" + q.Encode()))
	return h.Sum(nil)
}
