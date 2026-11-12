package aliyun

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
)

// algorithm 是阿里云 OpenAPI 的 V3 签名算法。
const algorithm = "ACS3-HMAC-SHA256"

// authorization 按 V3 签名计算 Authorization 请求头。参数都在请求体中，规范化查询字符串为空。
// headers 的键是小写的请求头名称，参与签名的是 host、content-type 和全部 x-acs- 开头的请求头。
func authorization(method string, headers map[string]string, payloadHash, accessKeyID, accessKeySecret string) string {
	var names []string
	for name := range headers {
		if name == "host" || name == "content-type" || strings.HasPrefix(name, "x-acs-") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	var canonical strings.Builder
	canonical.WriteString(method + "\n/\n\n")
	for _, name := range names {
		canonical.WriteString(name + ":" + strings.TrimSpace(headers[name]) + "\n")
	}
	signed := strings.Join(names, ";")
	canonical.WriteString("\n" + signed + "\n" + payloadHash)

	digest := sha256.Sum256([]byte(canonical.String()))
	mac := hmac.New(sha256.New, []byte(accessKeySecret))
	mac.Write([]byte(algorithm + "\n" + hex.EncodeToString(digest[:])))
	return algorithm + " Credential=" + accessKeyID + ",SignedHeaders=" + signed +
		",Signature=" + hex.EncodeToString(mac.Sum(nil))
}
