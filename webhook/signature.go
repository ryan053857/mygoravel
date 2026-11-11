package webhook

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"

	"github.com/ryan053857/mygoravel/errorx"
)

// ErrInvalidSignature 表示收到的 webhook 缺少签名请求头、签名不匹配或时间戳超出 Tolerance。
var ErrInvalidSignature = errorx.Define(errorx.Unauthenticated, "webhook.invalid_signature")

// Tolerance 是 Verify 接受的时间戳与当前时间的最大偏差，超出时视为重放。
const Tolerance = 5 * time.Minute

// 请求头名称，见 Standard Webhooks。
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"
)

const secretPrefix = "whsec_"

// newSecret 生成端点的签名密钥：whsec_ 加 24 字节随机数的 base64。
func newSecret() string {
	key := make([]byte, 24)
	_, _ = rand.Read(key) // crypto/rand.Read 不会返回错误
	return secretPrefix + base64.StdEncoding.EncodeToString(key)
}

func decodeSecret(secret string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(secret, secretPrefix))
	if err != nil || len(key) == 0 {
		return nil, errors.New("webhook: secret is not base64 with an optional whsec_ prefix")
	}
	return key, nil
}

// Sign 用 secret 为一次发送签名，返回 webhook-signature 请求头中的一项：v1,<签名>。
// 测试接收方的处理器时，用它构造带签名的请求。
func Sign(secret, id string, timestamp time.Time, body []byte) (string, error) {
	key, err := decodeSecret(secret)
	if err != nil {
		return "", err
	}
	return "v1," + signature(key, id, timestamp.Unix(), body), nil
}

func signature(key []byte, id string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + strconv.FormatInt(timestamp, 10) + "."))
	mac.Write(body)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// Verify 按 Standard Webhooks 校验收到的 webhook：header 中的任意一个 v1 签名与 secret 算出的一致，
// 并且时间戳与当前时间相差不超过 Tolerance。校验失败时返回 ErrInvalidSignature。
// body 必须是原始请求体，解析后再编码的 JSON 无法通过校验。
func Verify(secret string, header http.Header, body []byte) error {
	key, err := decodeSecret(secret)
	if err != nil {
		return err
	}
	id, ts, sigs := header.Get(HeaderID), header.Get(HeaderTimestamp), header.Get(HeaderSignature)
	if id == "" || ts == "" || sigs == "" {
		return ErrInvalidSignature.Wrap(errors.New("missing webhook headers"))
	}
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return ErrInvalidSignature.Wrap(errors.Newf("invalid timestamp %q", ts))
	}
	if d := time.Since(time.Unix(timestamp, 0)); d > Tolerance || d < -Tolerance {
		return ErrInvalidSignature.Wrap(errors.Newf("timestamp %s outside tolerance", ts))
	}
	want := []byte(signature(key, id, timestamp, body))
	for _, sig := range strings.Fields(sigs) {
		version, value, _ := strings.Cut(sig, ",")
		if version == "v1" && subtle.ConstantTimeCompare([]byte(value), want) == 1 {
			return nil
		}
	}
	return ErrInvalidSignature.Wrap(errors.New("no matching signature"))
}
