package tencent

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// algorithm 是腾讯云 API 3.0 的签名算法。
const algorithm = "TC3-HMAC-SHA256"

// signedHeaders 是参与签名的请求头，与官方 SDK 相同。
const signedHeaders = "content-type;host"

// authorization 计算 TC3-HMAC-SHA256 签名的 Authorization 请求头。参与签名的请求头是 content-type 和 host，
// 与官方 SDK 相同；查询字符串为空，参数都在 JSON 请求体中。
func authorization(host, contentType string, payload []byte, t time.Time, service, secretID, secretKey string) string {
	payloadHash := sha256.Sum256(payload)
	canonical := "POST\n/\n\n" +
		"content-type:" + contentType + "\n" +
		"host:" + host + "\n" +
		"\n" + signedHeaders + "\n" + hex.EncodeToString(payloadHash[:])

	date := t.UTC().Format("2006-01-02")
	scope := date + "/" + service + "/tc3_request"
	canonicalHash := sha256.Sum256([]byte(canonical))
	stringToSign := algorithm + "\n" + strconv.FormatInt(t.Unix(), 10) + "\n" + scope + "\n" + hex.EncodeToString(canonicalHash[:])

	key := hmacSHA256([]byte("TC3"+secretKey), date)
	key = hmacSHA256(key, service)
	key = hmacSHA256(key, "tc3_request")
	signature := hex.EncodeToString(hmacSHA256(key, stringToSign))
	return algorithm + " Credential=" + secretID + "/" + scope + ", SignedHeaders=" + signedHeaders + ", Signature=" + signature
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(data))
	return mac.Sum(nil)
}
